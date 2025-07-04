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

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/aws/amazon-ecs-agent/agent/dockerclient"
	"github.com/aws/amazon-ecs-agent/agent/utils"
	apierrors "github.com/aws/amazon-ecs-agent/ecs-agent/api/errors"
	"github.com/aws/amazon-ecs-agent/ecs-agent/ec2"
	commonutils "github.com/aws/amazon-ecs-agent/ecs-agent/utils"

	"github.com/cihub/seelog"
)

const (
	// http://www.iana.org/assignments/service-names-port-numbers/service-names-port-numbers.xhtml?search=docker
	DockerReservedPort    = 2375
	DockerReservedSSLPort = 2376
	// DockerTagSeparator is the charactor used to separate names and tag in docker
	DockerTagSeparator = ":"
	// DefaultDockerTag is the default tag used by docker
	DefaultDockerTag = "latest"

	SSHPort = 22

	// AgentIntrospectionPort is used to serve the metadata about the agent and to query the tasks being managed by the agent.
	AgentIntrospectionPort = 51678

	// AgentPrometheusExpositionPort is used to expose Prometheus metrics that can be scraped by a Prometheus server
	AgentPrometheusExpositionPort = 51680

	// defaultConfigFileName is the default (json-formatted) config file
	defaultConfigFileName = "/etc/ecs_container_agent/config.json"

	// DefaultClusterName is the name of the default cluster.
	DefaultClusterName = "default"

	// DefaultTaskCleanupWaitDuration specifies the default value for task cleanup duration. It is used to
	// clean up task's containers.
	DefaultTaskCleanupWaitDuration = 3 * time.Hour

	// DefaultPollingMetricsWaitDuration specifies the default value for polling metrics wait duration
	// This is only used when PollMetrics is set to true
	DefaultPollingMetricsWaitDuration = DefaultContainerMetricsPublishInterval / 2

	// defaultDockerStopTimeout specifies the value for container stop timeout duration
	defaultDockerStopTimeout = 30 * time.Second

	// DefaultImageCleanupTimeInterval specifies the default value for image cleanup duration. It is used to
	// remove the images pulled by agent.
	DefaultImageCleanupTimeInterval = 30 * time.Minute

	// DefaultNumImagesToDeletePerCycle specifies the default number of images to delete when agent performs
	// image cleanup.
	DefaultNumImagesToDeletePerCycle = 5

	// DefaultNumNonECSContainersToDeletePerCycle specifies the default number of nonecs containers to delete when agent performs
	// nonecs containers cleanup.
	DefaultNumNonECSContainersToDeletePerCycle = 5

	// DefaultImageDeletionAge specifies the default value for minimum amount of elapsed time after an image
	// has been pulled before it can be deleted.
	DefaultImageDeletionAge = 1 * time.Hour

	// DefaultNonECSImageDeletionAge specifies the default value for minimum amount of elapsed time after an image
	// has been created before it can be deleted
	DefaultNonECSImageDeletionAge = 1 * time.Hour

	//DefaultImagePullTimeout specifies the timeout for PullImage API.
	DefaultImagePullTimeout = 2 * time.Hour

	// minimumTaskCleanupWaitDuration specifies the minimum duration to wait before cleaning up
	// a task's container. This is used to enforce sane values for the config.TaskCleanupWaitDuration field.
	minimumTaskCleanupWaitDuration = time.Second

	// minimumImagePullInactivityTimeout specifies the minimum amount of time for that an image can be
	// 'stuck' in the pull / unpack step. Very small values are unsafe and lead to high failure rate.
	minimumImagePullInactivityTimeout = 1 * time.Minute

	// minimumPollingMetricsWaitDuration specifies the minimum duration to wait before polling for new stats
	// from docker. This is only used when PollMetrics is set to true
	minimumPollingMetricsWaitDuration = 5 * time.Second

	// maximumPollingMetricsWaitDuration specifies the maximum duration to wait before polling for new stats
	// from docker. This is only used when PollMetrics is set to true
	maximumPollingMetricsWaitDuration = DefaultContainerMetricsPublishInterval

	// minimumDockerStopTimeout specifies the minimum value for docker StopContainer API
	minimumDockerStopTimeout = 1 * time.Second

	// minimumImageCleanupInterval specifies the minimum time for agent to wait before performing
	// image cleanup.
	minimumImageCleanupInterval = 10 * time.Minute

	// minimumNumImagesToDeletePerCycle specifies the minimum number of images that to be deleted when
	// performing image cleanup.
	minimumNumImagesToDeletePerCycle = 1

	// defaultCNIPluginsPath is the default path where cni binaries are located
	defaultCNIPluginsPath = "/amazon-ecs-cni-plugins"

	// DefaultMinSupportedCNIVersion denotes the minimum version of cni spec required
	DefaultMinSupportedCNIVersion = "0.3.0"

	// pauseContainerTarball is the path to the pause container tarball
	pauseContainerTarballPath = "/images/amazon-ecs-pause.tar"

	// DefaultTaskMetadataSteadyStateRate is set as 40. This is arrived from our benchmarking
	// results where task endpoint can handle 4000 rps effectively. Here, 100 containers
	// will be able to send out 40 rps.
	DefaultTaskMetadataSteadyStateRate = 40

	// DefaultTaskMetadataBurstRate is set to handle 60 burst requests at once
	DefaultTaskMetadataBurstRate = 60

	//Known cached image names
	CachedImageNameAgentContainer = "amazon/amazon-ecs-agent:latest"

	// DefaultNvidiaRuntime is the name of the runtime to pass Nvidia GPUs to containers
	DefaultNvidiaRuntime = "nvidia"

	// defaultCgroupCPUPeriod is set to 100 ms to set isCFS period and quota for task limits
	defaultCgroupCPUPeriod = 100 * time.Millisecond
	maximumCgroupCPUPeriod = 100 * time.Millisecond
	minimumCgroupCPUPeriod = 8 * time.Millisecond

	// DefaultContainerMetricsPublishInterval is the default interval that we publish
	// metrics to the ECS telemetry backend (TACS)
	DefaultContainerMetricsPublishInterval = 20 * time.Second
)

const (
	// ImagePullDefaultBehavior specifies the behavior that if an image pull API call fails,
	// agent tries to start from the Docker image cache anyway, assuming that the image has not changed.
	ImagePullDefaultBehavior ImagePullBehaviorType = iota

	// ImagePullAlwaysBehavior specifies the behavior that if an image pull API call fails,
	// the task fails instead of using cached image.
	ImagePullAlwaysBehavior

	// ImagePullOnceBehavior specifies the behavior that agent will only attempt to pull
	// the same image once, once an image is pulled, local image cache will be used
	// for all the containers.
	ImagePullOnceBehavior

	// ImagePullPreferCachedBehavior specifies the behavior that agent will only attempt to pull
	// the image if there is no cached image.
	ImagePullPreferCachedBehavior
)

const (
	// When ContainerInstancePropagateTagsFromNoneType is specified, no DescribeTags
	// API call will be made.
	ContainerInstancePropagateTagsFromNoneType ContainerInstancePropagateTagsFromType = iota

	// When ContainerInstancePropagateTagsFromEC2InstanceType is specified, agent will
	// make DescribeTags API call to get tags remotely.
	ContainerInstancePropagateTagsFromEC2InstanceType
)

var (
	// DefaultPauseContainerImageName is the name of the pause container image. The linker's
	// load flags are used to populate this value from the Makefile
	DefaultPauseContainerImageName = ""

	// DefaultPauseContainerTag is the tag for the pause container image. The linker's load
	// flags are used to populate this value from the Makefile
	DefaultPauseContainerTag = ""

	// CgroupV2 Specifies whether or not to run in Cgroups V2 mode.
	CgroupV2 = false

	// isFIPSEnabled indicates whether FIPS mode is enabled on the host
	isFIPSEnabled = false
)

// Merge merges two config files, preferring the ones on the left. Any nil or
// zero values present in the left that are present in the right will be overridden
func (cfg *Config) Merge(rhs Config) *Config {
	left := reflect.ValueOf(cfg).Elem()
	right := reflect.ValueOf(&rhs).Elem()

	for i := 0; i < left.NumField(); i++ {
		leftField := left.Field(i)
		switch leftField.Interface().(type) {
		case BooleanDefaultFalse, BooleanDefaultTrue:
			str, _ := json.Marshal(reflect.ValueOf(leftField.Interface()).Interface())
			if string(str) == "null" {
				leftField.Set(reflect.ValueOf(right.Field(i).Interface()))
			}
		default:
			if commonutils.ZeroOrNil(leftField.Interface()) {
				leftField.Set(reflect.ValueOf(right.Field(i).Interface()))
			}
		}
	}

	return cfg //make it chainable
}

// NewConfig returns a config struct created by merging environment variables,
// a config file, and EC2 Metadata info.
// The 'config' struct it returns can be used, even if an error is returned. An
// error is returned, however, if the config is incomplete in some way that is
// considered fatal.
func NewConfig(ec2client ec2.EC2MetadataClient) (*Config, error) {
	var errs []error
	envConfig, err := environmentConfig() //Environment overrides all else
	if err != nil {
		errs = append(errs, err)
	}
	config := &envConfig
	isFIPSEnabled = utils.DetectFIPSMode(utils.FIPSModeFilePath)

	if config.External.Enabled() {
		if config.AWSRegion == "" {
			return nil, errors.New("AWS_DEFAULT_REGION has to be set when running on external capacity")
		}
		// Use fake ec2 metadata client if on prem config is set.
		ec2client = ec2.NewBlackholeEC2MetadataClient()
	}

	// TODO feat:IPv6-only - Enable when launching IPv6-only support
	// config.determineIPCompatibility(ec2client)

	if config.complete() {
		// No need to do file / network IO
		return config, nil
	}

	fcfg, err := fileConfig()
	if err != nil {
		errs = append(errs, err)
	}
	config.Merge(fcfg)

	config.Merge(userDataConfig(ec2client))

	if config.AWSRegion == "" {
		if config.NoIID {
			// get it from AWS SDK if we don't have instance identity document
			awsRegion, err := ec2client.Region()
			if err != nil {
				errs = append(errs, err)
			}
			config.AWSRegion = awsRegion
		} else {
			// Get it from metadata only if we need to (network io)
			config.Merge(ec2MetadataConfig(ec2client))
		}
	}

	return config, config.mergeDefaultConfig(errs)
}

func (config *Config) mergeDefaultConfig(errs []error) error {
	config.trimWhitespace()
	config.Merge(DefaultConfig(config.InstanceIPCompatibility))
	err := config.validateAndOverrideBounds()
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) != 0 {
		return apierrors.NewMultiError(errs...)
	}
	return nil
}

// trimWhitespace trims whitespace from all string cfg values with the
// `trim` tag
func (cfg *Config) trimWhitespace() {
	cfgElem := reflect.ValueOf(cfg).Elem()
	cfgStructField := reflect.Indirect(reflect.ValueOf(cfg)).Type()

	for i := 0; i < cfgElem.NumField(); i++ {
		cfgField := cfgElem.Field(i)
		if !cfgField.CanInterface() {
			continue
		}
		trimTag := cfgStructField.Field(i).Tag.Get("trim")
		if len(trimTag) == 0 {
			continue
		}

		if cfgField.Kind() != reflect.String {
			seelog.Warnf("Cannot trim non-string field type %v index %v", cfgField.Kind().String(), i)
			continue
		}
		str := cfgField.Interface().(string)
		cfgField.SetString(strings.TrimSpace(str))
	}
}

// validateAndOverrideBounds performs validation over members of the Config struct
// and check the value against the minimum required value.
func (cfg *Config) validateAndOverrideBounds() error {
	err := cfg.checkMissingAndDepreciated()
	if err != nil {
		return err
	}

	if cfg.DockerStopTimeout < minimumDockerStopTimeout {
		return fmt.Errorf("config: invalid value for docker container stop timeout: %v", cfg.DockerStopTimeout.String())
	}

	if cfg.ContainerStartTimeout < minimumContainerStartTimeout {
		return fmt.Errorf("config: invalid value for docker container start timeout: %v", cfg.ContainerStartTimeout.String())
	}
	var badDrivers []string
	for _, driver := range cfg.AvailableLoggingDrivers {
		// Don't classify awsfirelens as a bad driver
		if driver == dockerclient.AWSFirelensDriver {
			continue
		}
		_, ok := dockerclient.LoggingDriverMinimumVersion[driver]
		if !ok {
			badDrivers = append(badDrivers, string(driver))
		}
	}
	if len(badDrivers) > 0 {
		return errors.New("Invalid logging drivers: " + strings.Join(badDrivers, ", "))
	}

	// If a value has been set for taskCleanupWaitDuration and the value is less than the minimum allowed cleanup duration,
	// print a warning and override it
	if cfg.TaskCleanupWaitDuration < minimumTaskCleanupWaitDuration {
		seelog.Warnf("Invalid value for ECS_ENGINE_TASK_CLEANUP_WAIT_DURATION, will be overridden with the default value: %s. Parsed value: %v, minimum value: %v.", DefaultTaskCleanupWaitDuration.String(), cfg.TaskCleanupWaitDuration, minimumTaskCleanupWaitDuration)
		cfg.TaskCleanupWaitDuration = DefaultTaskCleanupWaitDuration
	}

	if cfg.ImagePullInactivityTimeout < minimumImagePullInactivityTimeout {
		seelog.Warnf("Invalid value for image pull inactivity timeout duration, will be overridden with the default value: %s. Parsed value: %v, minimum value: %v.", defaultImagePullInactivityTimeout.String(), cfg.ImagePullInactivityTimeout, minimumImagePullInactivityTimeout)
		cfg.ImagePullInactivityTimeout = defaultImagePullInactivityTimeout
	}

	if cfg.ImageCleanupInterval < minimumImageCleanupInterval {
		seelog.Warnf("Invalid value for ECS_IMAGE_CLEANUP_INTERVAL, will be overridden with the default value: %s. Parsed value: %v, minimum value: %v.", DefaultImageCleanupTimeInterval.String(), cfg.ImageCleanupInterval, minimumImageCleanupInterval)
		cfg.ImageCleanupInterval = DefaultImageCleanupTimeInterval
	}

	if cfg.NumImagesToDeletePerCycle < minimumNumImagesToDeletePerCycle {
		seelog.Warnf("Invalid value for number of images to delete for image cleanup, will be overridden with the default value: %d. Parsed value: %d, minimum value: %d.", DefaultImageDeletionAge, cfg.NumImagesToDeletePerCycle, minimumNumImagesToDeletePerCycle)
		cfg.NumImagesToDeletePerCycle = DefaultNumImagesToDeletePerCycle
	}

	if cfg.TaskMetadataSteadyStateRate <= 0 || cfg.TaskMetadataBurstRate <= 0 {
		seelog.Warnf("Invalid values for rate limits, will be overridden with default values: %d,%d.", DefaultTaskMetadataSteadyStateRate, DefaultTaskMetadataBurstRate)
		cfg.TaskMetadataSteadyStateRate = DefaultTaskMetadataSteadyStateRate
		cfg.TaskMetadataBurstRate = DefaultTaskMetadataBurstRate
	}

	// check the PollMetrics specific configurations
	cfg.pollMetricsOverrides()

	cfg.platformOverrides()

	return nil
}

func (cfg *Config) pollMetricsOverrides() {
	if cfg.PollMetrics.Enabled() {
		if cfg.PollingMetricsWaitDuration < minimumPollingMetricsWaitDuration {
			seelog.Warnf("ECS_POLLING_METRICS_WAIT_DURATION parsed value (%s) is less than the minimum of %s. Setting polling interval to minimum.",
				cfg.PollingMetricsWaitDuration, minimumPollingMetricsWaitDuration)
			cfg.PollingMetricsWaitDuration = minimumPollingMetricsWaitDuration
		}

		if cfg.PollingMetricsWaitDuration > maximumPollingMetricsWaitDuration {
			seelog.Warnf("ECS_POLLING_METRICS_WAIT_DURATION parsed value (%s) is greater than the maximum of %s. Setting polling interval to maximum.",
				cfg.PollingMetricsWaitDuration, maximumPollingMetricsWaitDuration)
			cfg.PollingMetricsWaitDuration = maximumPollingMetricsWaitDuration
		}
	}
}

// checkMissingAndDeprecated checks all zero-valued fields for tags of the form
// missing:STRING and acts based on that string. Current options are: fatal,
// warn. Fatal will result in an error being returned, warn will result in a
// warning that the field is missing being logged.
func (cfg *Config) checkMissingAndDepreciated() error {
	cfgElem := reflect.ValueOf(cfg).Elem()
	cfgStructField := reflect.Indirect(reflect.ValueOf(cfg)).Type()

	fatalFields := []string{}
	for i := 0; i < cfgElem.NumField(); i++ {
		cfgField := cfgElem.Field(i)
		if commonutils.ZeroOrNil(cfgField.Interface()) {
			missingTag := cfgStructField.Field(i).Tag.Get("missing")
			if len(missingTag) == 0 {
				continue
			}
			switch missingTag {
			case "warn":
				seelog.Warnf("Configuration key not set, key: %v", cfgStructField.Field(i).Name)
			case "fatal":
				seelog.Criticalf("Configuration key not set, key: %v", cfgStructField.Field(i).Name)
				fatalFields = append(fatalFields, cfgStructField.Field(i).Name)
			default:
				seelog.Warnf("Unexpected `missing` tag value, tag %v", missingTag)
			}
		} else {
			// present
			deprecatedTag := cfgStructField.Field(i).Tag.Get("deprecated")
			if len(deprecatedTag) == 0 {
				continue
			}
			seelog.Warnf("Use of deprecated configuration key, key: %v message: %v", cfgStructField.Field(i).Name, deprecatedTag)
		}
	}
	if len(fatalFields) > 0 {
		return errors.New("Missing required fields: " + strings.Join(fatalFields, ", "))
	}
	return nil
}

// complete returns true if all fields of the config are populated / nonzero
func (cfg *Config) complete() bool {
	cfgElem := reflect.ValueOf(cfg).Elem()

	for i := 0; i < cfgElem.NumField(); i++ {
		if commonutils.ZeroOrNil(cfgElem.Field(i).Interface()) {
			return false
		}
	}
	return true
}

func fileConfig() (Config, error) {
	cfg := Config{}
	fileName, err := getConfigFileName()
	if err != nil {
		return cfg, nil
	}

	file, err := os.Open(fileName)
	if err != nil {
		return cfg, nil
	}
	data, err := ioutil.ReadAll(file)
	if err != nil {
		seelog.Errorf("Unable to read cfg file, err %v", err)
		return cfg, err
	}
	if strings.TrimSpace(string(data)) == "" {
		// empty file, not an error
		return cfg, nil
	}

	err = json.Unmarshal(data, &cfg)
	if err != nil {
		seelog.Criticalf("Error reading cfg json data, err %v", err)
		return cfg, err
	}

	// Handle any deprecated keys correctly here
	if commonutils.ZeroOrNil(cfg.Cluster) && !commonutils.ZeroOrNil(cfg.ClusterArn) {
		cfg.Cluster = cfg.ClusterArn
	}
	return cfg, nil
}

// userDataConfig reads configuration JSON from instance's userdata. It doesn't
// return any error as it's entirely optional to configure the ECS agent using
// this method.
// Example:
// {"ECSAgentConfiguration":{"Cluster":"default"}}
func userDataConfig(ec2Client ec2.EC2MetadataClient) Config {
	type userDataParser struct {
		Config Config `json:"ECSAgentConfiguration"`
	}

	parsedUserData := userDataParser{
		Config: Config{},
	}

	userData, err := ec2Client.GetUserData()
	if err != nil {
		seelog.Warnf("Unable to fetch user data: %v", err)
		// Unable to read userdata from instance metadata. Just
		// return early
		return parsedUserData.Config
	}
	// In the future, if we want to support base64 encoded config,
	// we'd need to add logic to decode the string here.
	err = json.Unmarshal([]byte(userData), &parsedUserData)
	if err != nil {
		seelog.Debugf("Non-json user data, skip merging into agent config: %v", err)
		// Unable to parse userdata as a valid JSON. Return the
		// empty config
		return Config{}
	}

	return parsedUserData.Config
}

// environmentConfig reads the given configs from the environment and attempts
// to convert them to the given type
func environmentConfig() (Config, error) {
	dataDir := os.Getenv("ECS_DATADIR")

	steadyStateRate, burstRate := parseTaskMetadataThrottles()

	var errs []error
	instanceAttributes, errs := parseInstanceAttributes(errs)

	containerInstanceTags, errs := parseContainerInstanceTags(errs)

	additionalLocalRoutes, errs := parseAdditionalLocalRoutes(errs)

	var err error
	if len(errs) > 0 {
		err = apierrors.NewMultiError(errs...)
	}
	return Config{
		Cluster:                             os.Getenv("ECS_CLUSTER"),
		APIEndpoint:                         os.Getenv("ECS_BACKEND_HOST"),
		AWSRegion:                           os.Getenv("AWS_DEFAULT_REGION"),
		DockerEndpoint:                      os.Getenv("DOCKER_HOST"),
		ReservedPorts:                       parseReservedPorts("ECS_RESERVED_PORTS"),
		ReservedPortsUDP:                    parseReservedPorts("ECS_RESERVED_PORTS_UDP"),
		DataDir:                             dataDir,
		Checkpoint:                          parseCheckpoint(dataDir),
		EngineAuthType:                      os.Getenv("ECS_ENGINE_AUTH_TYPE"),
		EngineAuthData:                      NewSensitiveRawMessage([]byte(os.Getenv("ECS_ENGINE_AUTH_DATA"))),
		UpdatesEnabled:                      parseBooleanDefaultFalseConfig("ECS_UPDATES_ENABLED"),
		UpdateDownloadDir:                   os.Getenv("ECS_UPDATE_DOWNLOAD_DIR"),
		DisableMetrics:                      parseBooleanDefaultFalseConfig("ECS_DISABLE_METRICS"),
		ReservedMemory:                      parseEnvVariableUint16("ECS_RESERVED_MEMORY"),
		AvailableLoggingDrivers:             parseAvailableLoggingDrivers(),
		PrivilegedDisabled:                  parseBooleanDefaultFalseConfig("ECS_DISABLE_PRIVILEGED"),
		SELinuxCapable:                      parseBooleanDefaultFalseConfig("ECS_SELINUX_CAPABLE"),
		AppArmorCapable:                     parseBooleanDefaultFalseConfig("ECS_APPARMOR_CAPABLE"),
		TaskCleanupWaitDuration:             parseEnvVariableDuration("ECS_ENGINE_TASK_CLEANUP_WAIT_DURATION"),
		TaskCleanupWaitDurationJitter:       parseEnvVariableDuration("ECS_ENGINE_TASK_CLEANUP_WAIT_DURATION_JITTER"),
		TaskENIEnabled:                      parseBooleanDefaultFalseConfig("ECS_ENABLE_TASK_ENI"),
		TaskIAMRoleEnabled:                  parseBooleanDefaultFalseConfig("ECS_ENABLE_TASK_IAM_ROLE"),
		DeleteNonECSImagesEnabled:           parseBooleanDefaultFalseConfig("ECS_ENABLE_UNTRACKED_IMAGE_CLEANUP"),
		TaskCPUMemLimit:                     parseBooleanDefaultTrueConfig("ECS_ENABLE_TASK_CPU_MEM_LIMIT"),
		DockerStopTimeout:                   parseDockerStopTimeout(),
		ManifestPullTimeout:                 parseManifestPullTimeout(),
		ContainerStartTimeout:               parseContainerStartTimeout(),
		ContainerCreateTimeout:              parseContainerCreateTimeout(),
		DependentContainersPullUpfront:      parseBooleanDefaultFalseConfig("ECS_PULL_DEPENDENT_CONTAINERS_UPFRONT"),
		ImagePullInactivityTimeout:          parseImagePullInactivityTimeout(),
		ImagePullTimeout:                    parseEnvVariableDuration("ECS_IMAGE_PULL_TIMEOUT"),
		CredentialsAuditLogFile:             os.Getenv("ECS_AUDIT_LOGFILE"),
		CredentialsAuditLogDisabled:         utils.ParseBool(os.Getenv("ECS_AUDIT_LOGFILE_DISABLED"), false),
		TaskIAMRoleEnabledForNetworkHost:    utils.ParseBool(os.Getenv("ECS_ENABLE_TASK_IAM_ROLE_NETWORK_HOST"), false),
		ImageCleanupDisabled:                parseBooleanDefaultFalseConfig("ECS_DISABLE_IMAGE_CLEANUP"),
		MinimumImageDeletionAge:             parseEnvVariableDuration("ECS_IMAGE_MINIMUM_CLEANUP_AGE"),
		NonECSMinimumImageDeletionAge:       parseEnvVariableDuration("NON_ECS_IMAGE_MINIMUM_CLEANUP_AGE"),
		ImageCleanupInterval:                parseEnvVariableDuration("ECS_IMAGE_CLEANUP_INTERVAL"),
		NumImagesToDeletePerCycle:           parseNumImagesToDeletePerCycle(),
		NumNonECSContainersToDeletePerCycle: parseNumNonECSContainersToDeletePerCycle(),
		ImagePullBehavior:                   parseImagePullBehavior(),
		ImageCleanupExclusionList:           parseImageCleanupExclusionList("ECS_EXCLUDE_UNTRACKED_IMAGE"),
		InstanceAttributes:                  instanceAttributes,
		CNIPluginsPath:                      os.Getenv("ECS_CNI_PLUGINS_PATH"),
		AWSVPCBlockInstanceMetdata:          parseBooleanDefaultFalseConfig("ECS_AWSVPC_BLOCK_IMDS"),
		AWSVPCAdditionalLocalRoutes:         additionalLocalRoutes,
		ContainerMetadataEnabled:            parseBooleanDefaultFalseConfig("ECS_ENABLE_CONTAINER_METADATA"),
		DataDirOnHost:                       os.Getenv("ECS_HOST_DATA_DIR"),
		OverrideAWSLogsExecutionRole:        parseBooleanDefaultFalseConfig("ECS_ENABLE_AWSLOGS_EXECUTIONROLE_OVERRIDE"),
		CgroupPath:                          os.Getenv("ECS_CGROUP_PATH"),
		TaskMetadataSteadyStateRate:         steadyStateRate,
		TaskMetadataBurstRate:               burstRate,
		SharedVolumeMatchFullConfig:         parseBooleanDefaultFalseConfig("ECS_SHARED_VOLUME_MATCH_FULL_CONFIG"),
		ContainerInstanceTags:               containerInstanceTags,
		ContainerInstancePropagateTagsFrom:  parseContainerInstancePropagateTagsFrom(),
		PollMetrics:                         parseBooleanDefaultFalseConfig("ECS_POLL_METRICS"),
		PollingMetricsWaitDuration:          parseEnvVariableDuration("ECS_POLLING_METRICS_WAIT_DURATION"),
		DisableDockerHealthCheck:            parseBooleanDefaultFalseConfig("ECS_DISABLE_DOCKER_HEALTH_CHECK"),
		GPUSupportEnabled:                   utils.ParseBool(os.Getenv("ECS_ENABLE_GPU_SUPPORT"), false),
		EBSTASupportEnabled:                 utils.ParseBool(os.Getenv("ECS_EBSTA_SUPPORTED"), true),
		InferentiaSupportEnabled:            utils.ParseBool(os.Getenv("ECS_ENABLE_INF_SUPPORT"), false),
		NvidiaRuntime:                       os.Getenv("ECS_NVIDIA_RUNTIME"),
		TaskMetadataAZDisabled:              utils.ParseBool(os.Getenv("ECS_DISABLE_TASK_METADATA_AZ"), false),
		CgroupCPUPeriod:                     parseCgroupCPUPeriod(),
		SpotInstanceDrainingEnabled:         parseBooleanDefaultFalseConfig("ECS_ENABLE_SPOT_INSTANCE_DRAINING"),
		GMSACapable:                         parseGMSACapability(),
		GMSADomainlessCapable:               parseGMSADomainlessCapability(),
		VolumePluginCapabilities:            parseVolumePluginCapabilities(),
		FSxWindowsFileServerCapable:         parseFSxWindowsFileServerCapability(),
		External:                            parseBooleanDefaultFalseConfig("ECS_EXTERNAL"),
		EnableRuntimeStats:                  parseBooleanDefaultFalseConfig("ECS_ENABLE_RUNTIME_STATS"),
		ShouldExcludeIPv6PortBinding:        parseBooleanDefaultTrueConfig("ECS_EXCLUDE_IPV6_PORTBINDING"),
		WarmPoolsSupport:                    parseBooleanDefaultFalseConfig("ECS_WARM_POOLS_CHECK"),
		DynamicHostPortRange:                parseDynamicHostPortRange("ECS_DYNAMIC_HOST_PORT_RANGE"),
		TaskPidsLimit:                       parseTaskPidsLimit(),
		FirelensAsyncEnabled:                parseBooleanDefaultTrueConfig("ECS_ENABLE_FIRELENS_ASYNC"),
	}, err
}

func ec2MetadataConfig(ec2client ec2.EC2MetadataClient) Config {
	iid, err := ec2client.InstanceIdentityDocument()
	if err != nil {
		seelog.Criticalf("Unable to communicate with EC2 Metadata service to infer region: %v", err.Error())
		return Config{}
	}
	return Config{AWSRegion: iid.Region}
}

// String returns a lossy string representation of the config suitable for human readable display.
// Consequently, it *should not* return any sensitive information.
func (cfg *Config) String() string {
	return fmt.Sprintf(
		"Cluster: %v, "+
			" Region: %v, "+
			" DataDir: %v,"+
			" Checkpoint: %v, "+
			"AuthType: %v, "+
			"UpdatesEnabled: %v, "+
			"DisableMetrics: %v, "+
			"PollMetrics: %v, "+
			"PollingMetricsWaitDuration: %v, "+
			"ReservedMem: %v, "+
			"TaskCleanupWaitDuration: %v, "+
			"ManifestPullTimeout: %v, "+
			"DockerStopTimeout: %v, "+
			"ContainerStartTimeout: %v, "+
			"ContainerCreateTimeout: %v, "+
			"DependentContainersPullUpfront: %v, "+
			"TaskCPUMemLimit: %v, "+
			"ShouldExcludeIPv6PortBinding: %v, "+
			"DynamicHostPortRange: %v"+
			"%s",
		cfg.Cluster,
		cfg.AWSRegion,
		cfg.DataDir,
		cfg.Checkpoint,
		cfg.EngineAuthType,
		cfg.UpdatesEnabled,
		cfg.DisableMetrics,
		cfg.PollMetrics,
		cfg.PollingMetricsWaitDuration,
		cfg.ReservedMemory,
		cfg.TaskCleanupWaitDuration,
		cfg.ManifestPullTimeout,
		cfg.DockerStopTimeout,
		cfg.ContainerStartTimeout,
		cfg.ContainerCreateTimeout,
		cfg.DependentContainersPullUpfront,
		cfg.TaskCPUMemLimit,
		cfg.ShouldExcludeIPv6PortBinding,
		cfg.DynamicHostPortRange,
		cfg.platformString(),
	)
}

func IsFIPSEnabled() bool {
	return isFIPSEnabled
}

// SetFIPSEnabled sets the isFIPSEnabled variable for testing purposes
// that is used in s3/factory/factory_test.go
func SetFIPSEnabled(enabled bool) {
	isFIPSEnabled = enabled
}
