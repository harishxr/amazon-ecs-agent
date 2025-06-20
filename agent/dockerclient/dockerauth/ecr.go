// Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//	http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package dockerauth

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	apicontainer "github.com/aws/amazon-ecs-agent/agent/api/container"
	"github.com/aws/amazon-ecs-agent/agent/ecr"
	"github.com/aws/amazon-ecs-agent/ecs-agent/async"
	"github.com/aws/amazon-ecs-agent/ecs-agent/credentials"
	"github.com/aws/amazon-ecs-agent/ecs-agent/logger"
	"github.com/aws/amazon-ecs-agent/ecs-agent/logger/field"
	"github.com/aws/amazon-ecs-agent/ecs-agent/utils/retry"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
	log "github.com/cihub/seelog"
	"github.com/docker/docker/api/types/registry"
)

type cacheKey struct {
	region           string
	roleARN          string
	registryID       string
	endpointOverride string
	credentialsID    string
}

type ecrAuthProvider struct {
	tokenCache async.Cache
	factory    ecr.ECRFactory
}

const (
	// MinimumJitterDuration is the minimum duration to mark the credentials
	// as expired before it's actually expired
	MinimumJitterDuration = 30 * time.Minute
	roundtripTimeout      = 5 * time.Second
	proxyEndpointScheme   = "https://"
)

// String formats the cachKey as a string
func (key *cacheKey) String() string {
	return fmt.Sprintf("%s%s-%s-%s-%s",
		key.credentialsID, key.roleARN, key.region, key.registryID, key.endpointOverride)
}

// NewECRAuthProvider returns a DockerAuthProvider that can handle retrieve
// credentials for pulling from Amazon EC2 Container Registry
func NewECRAuthProvider(ecrFactory ecr.ECRFactory, cache async.Cache) DockerAuthProvider {
	return &ecrAuthProvider{
		tokenCache: cache,
		factory:    ecrFactory,
	}
}

// GetAuthconfig retrieves the correct auth configuration for the given repository
func (authProvider *ecrAuthProvider) GetAuthconfig(image string,
	registryAuthData *apicontainer.RegistryAuthenticationData) (registry.AuthConfig, error) {

	if registryAuthData == nil {
		return registry.AuthConfig{}, fmt.Errorf("dockerauth: missing container's registry auth data")
	}

	authData := registryAuthData.ECRAuthData

	if authData == nil {
		return registry.AuthConfig{}, fmt.Errorf("dockerauth: missing container's ecr auth data")
	}

	// First try to get the token from cache, if the token does not exist,
	// then call ECR api to get the new token
	key := cacheKey{
		region:           authData.Region,
		endpointOverride: authData.EndpointOverride,
		registryID:       authData.RegistryID,
	}

	// If the container is using execution role credentials to pull,
	// add the roleARN as part of the cache key so that docker auth for
	// containers pull with the same role can be cached
	if authData.GetPullCredentials() != (credentials.IAMRoleCredentials{}) {
		key.roleARN = authData.GetPullCredentials().RoleArn
		key.credentialsID = authData.GetPullCredentials().CredentialsID
	}

	// Try to get the auth config from cache
	auth := authProvider.getAuthConfigFromCache(key)
	if auth != nil {
		return *auth, nil
	}

	// Get the auth config from ECR
	return authProvider.getAuthConfigFromECR(image, key, authData)
}

// getAuthconfigFromCache retrieves the token from cache
func (authProvider *ecrAuthProvider) getAuthConfigFromCache(key cacheKey) *registry.AuthConfig {
	token, ok := authProvider.tokenCache.Get(key.String())
	if !ok {
		return nil
	}

	cachedToken, ok := token.(*types.AuthorizationData)
	if !ok {
		log.Warnf("Reading ECR credentials from cache failed")
		return nil
	}

	if authProvider.IsTokenValid(cachedToken) {
		auth, err := extractToken(cachedToken)
		if err != nil {
			log.Errorf("Extract docker auth from cache failed, err: %v", err)
			// Remove invalid token from cache
			authProvider.tokenCache.Delete(key.String())
			return nil
		}
		return &auth
	} else {
		// Remove invalid token from cache
		authProvider.tokenCache.Delete(key.String())
	}
	return nil
}

// getAuthConfigFromECR calls the ECR API to get docker auth config
func (authProvider *ecrAuthProvider) getAuthConfigFromECR(image string, key cacheKey, authData *apicontainer.ECRAuthData) (registry.AuthConfig, error) {
	// Create ECR client to get the token
	client, err := authProvider.factory.GetClient(authData)
	if err != nil {
		return registry.AuthConfig{}, err
	}

	logger.Debug("Calling ECR.GetAuthorizationToken", logger.Fields{
		field.Image:         image,
		field.CredentialsID: key.credentialsID,
		field.RegistryID:    key.registryID,
		field.RoleARN:       key.roleARN,
		"endpointOverride":  key.endpointOverride,
		"region":            key.region,
	})
	ecrAuthData, err := client.GetAuthorizationToken(authData.RegistryID)
	if err != nil {
		return registry.AuthConfig{}, err
	}
	if ecrAuthData == nil {
		return registry.AuthConfig{}, fmt.Errorf("ecr auth: missing AuthorizationData in ECR response for %s", image)
	}

	if ecrAuthData.AuthorizationToken != nil {
		// Cache the new token
		authProvider.tokenCache.Set(key.String(), ecrAuthData)
		return extractToken(ecrAuthData)
	}

	return registry.AuthConfig{}, fmt.Errorf("ecr auth: AuthorizationData is nil for %s", image)
}

func extractToken(authData *types.AuthorizationData) (registry.AuthConfig, error) {
	decodedToken, err := base64.StdEncoding.DecodeString(aws.ToString(authData.AuthorizationToken))
	if err != nil {
		return registry.AuthConfig{}, err
	}
	parts := strings.SplitN(string(decodedToken), ":", 2)
	return registry.AuthConfig{
		Username:      parts[0],
		Password:      parts[1],
		ServerAddress: aws.ToString(authData.ProxyEndpoint),
	}, nil
}

// IsTokenValid checks the token is still within it's expiration window. We early expire to allow
// for timing in calls and add jitter to avoid refreshing all of the tokens at once.
func (authProvider *ecrAuthProvider) IsTokenValid(authData *types.AuthorizationData) bool {
	if authData == nil || authData.ExpiresAt == nil {
		return false
	}

	refreshTime := aws.ToTime(authData.ExpiresAt).
		Add(-1 * retry.AddJitter(MinimumJitterDuration, MinimumJitterDuration))

	return time.Now().Before(refreshTime)
}
