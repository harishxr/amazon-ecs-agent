//go:build linux && unit
// +build linux,unit

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

package app

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	app_mocks "github.com/aws/amazon-ecs-agent/agent/app/mocks"
	"github.com/aws/amazon-ecs-agent/agent/config"
	"github.com/aws/amazon-ecs-agent/agent/data"
	"github.com/aws/amazon-ecs-agent/agent/dockerclient/dockerapi"
	"github.com/aws/amazon-ecs-agent/agent/ecscni"
	mock_ecscni "github.com/aws/amazon-ecs-agent/agent/ecscni/mocks"
	"github.com/aws/amazon-ecs-agent/agent/engine"
	"github.com/aws/amazon-ecs-agent/agent/engine/dockerstate"
	mock_dockerstate "github.com/aws/amazon-ecs-agent/agent/engine/dockerstate/mocks"
	mock_engine "github.com/aws/amazon-ecs-agent/agent/engine/mocks"
	mock_serviceconnect "github.com/aws/amazon-ecs-agent/agent/engine/serviceconnect/mock"
	mock_udev "github.com/aws/amazon-ecs-agent/agent/eni/udevwrapper/mocks"
	"github.com/aws/amazon-ecs-agent/agent/eni/watcher"
	mock_gpu "github.com/aws/amazon-ecs-agent/agent/gpu/mocks"
	"github.com/aws/amazon-ecs-agent/agent/sighandlers/exitcodes"
	"github.com/aws/amazon-ecs-agent/agent/taskresource"
	"github.com/aws/amazon-ecs-agent/agent/taskresource/cgroup/control/mock_control"
	mock_loader "github.com/aws/amazon-ecs-agent/agent/utils/loader/mocks"
	mock_mobypkgwrapper "github.com/aws/amazon-ecs-agent/agent/utils/mobypkgwrapper/mocks"
	mock_ec2 "github.com/aws/amazon-ecs-agent/ecs-agent/ec2/mocks"
	"github.com/aws/amazon-ecs-agent/ecs-agent/eventstream"
	"github.com/aws/amazon-ecs-agent/ecs-agent/ipcompatibility"
	md "github.com/aws/amazon-ecs-agent/ecs-agent/manageddaemon"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/smithy-go"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
)

const (
	mac      = "01:23:45:67:89:ab"
	vpcID    = "vpc-1234"
	subnetID = "subnet-1234"
)

func resetGetpid() {
	getPid = os.Getpid
}

func TestDoStartTaskENIHappyPath(t *testing.T) {
	ctrl, credentialsManager, _, imageManager, client,
		dockerClient, _, _, execCmdMgr, _ := setup(t)
	defer ctrl.Finish()

	cniCapabilities := []string{ecscni.CapabilityAWSVPCNetworkingMode}
	containerChangeEvents := make(chan dockerapi.DockerContainerChangeEvent)
	monitoShutdownEvents := make(chan bool)

	cniClient := mock_ecscni.NewMockCNIClient(ctrl)
	mockCredentialsProvider := app_mocks.NewMockCredentialsProvider(ctrl)
	mockPauseLoader := mock_loader.NewMockLoader(ctrl)
	mockUdevMonitor := mock_udev.NewMockUdev(ctrl)
	mockMetadata := mock_ec2.NewMockEC2MetadataClient(ctrl)
	mockMobyPlugins := mock_mobypkgwrapper.NewMockPlugins(ctrl)

	eniWatcher := &watcher.ENIWatcher{}
	eniWatcher.InjectFields(mockUdevMonitor)

	var discoverEndpointsInvoked sync.WaitGroup
	discoverEndpointsInvoked.Add(2)

	// These calls are expected to happen, but cannot be ordered as they are
	// invoked via go routines, which will lead to occasional test failues
	dockerClient.EXPECT().Version(gomock.Any(), gomock.Any()).AnyTimes()
	dockerClient.EXPECT().SupportedVersions().Return(apiVersions).AnyTimes()
	dockerClient.EXPECT().ListContainers(gomock.Any(), gomock.Any(), gomock.Any()).Return(
		dockerapi.ListContainersResponse{}).AnyTimes()
	imageManager.EXPECT().StartImageCleanupProcess(gomock.Any()).MaxTimes(1)
	client.EXPECT().DiscoverPollEndpoint(gomock.Any()).Do(func(x interface{}) {
		// Ensures that the test waits until acs session has bee started
		discoverEndpointsInvoked.Done()
	}).Return("poll-endpoint", nil)
	client.EXPECT().DiscoverPollEndpoint(gomock.Any()).Return("acs-endpoint", nil).AnyTimes()
	client.EXPECT().DiscoverTelemetryEndpoint(gomock.Any()).Do(func(x interface{}) {
		// Ensures that the test waits until telemetry session has bee started
		discoverEndpointsInvoked.Done()
	}).Return("telemetry-endpoint", nil)
	client.EXPECT().DiscoverTelemetryEndpoint(gomock.Any()).Return(
		"tele-endpoint", nil).AnyTimes()
	mockMetadata.EXPECT().OutpostARN().Return("", nil)
	mockPauseLoader.EXPECT().LoadImage(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	mockPauseLoader.EXPECT().IsLoaded(gomock.Any()).Return(true, nil).AnyTimes()
	mockServiceConnectManager := mock_serviceconnect.NewMockManager(ctrl)
	mockServiceConnectManager.EXPECT().IsLoaded(gomock.Any()).Return(true, nil).AnyTimes()
	mockServiceConnectManager.EXPECT().GetLoadedAppnetVersion().AnyTimes()
	mockServiceConnectManager.EXPECT().GetCapabilitiesForAppnetInterfaceVersion("").AnyTimes()
	mockServiceConnectManager.EXPECT().SetECSClient(gomock.Any(), gomock.Any()).AnyTimes()
	mockServiceConnectManager.EXPECT().GetAppnetContainerTarballDir().AnyTimes()
	mockServiceConnectManager.EXPECT().GetLoadedImageName().Return("service_connect_agent:v1").AnyTimes()

	originalImportAll := md.ImportAll
	defer func() { md.ImportAll = originalImportAll }()
	md.ImportAll = func() ([]*md.ManagedDaemon, error) {
		return []*md.ManagedDaemon{}, nil
	}

	imageManager.EXPECT().AddImageToCleanUpExclusionList(gomock.Eq("service_connect_agent:v1")).Times(1)
	mockUdevMonitor.EXPECT().Monitor(gomock.Any()).Return(monitoShutdownEvents).AnyTimes()
	client.EXPECT().GetHostResources().Return(testHostResource, nil).Times(1)

	gomock.InOrder(
		mockMetadata.EXPECT().PrimaryENIMAC().Return(mac, nil),
		mockMetadata.EXPECT().VPCID(mac).Return(vpcID, nil),
		mockMetadata.EXPECT().SubnetID(mac).Return(subnetID, nil),
		cniClient.EXPECT().Capabilities(ecscni.VPCENIPluginName).Return(cniCapabilities, nil),
		cniClient.EXPECT().Capabilities(ecscni.ECSBridgePluginName).Return(cniCapabilities, nil),
		cniClient.EXPECT().Capabilities(ecscni.ECSIPAMPluginName).Return(cniCapabilities, nil),
		cniClient.EXPECT().Capabilities(ecscni.ECSAppMeshPluginName).Return(cniCapabilities, nil),
		cniClient.EXPECT().Capabilities(ecscni.ECSBranchENIPluginName).Return(cniCapabilities, nil),
		mockCredentialsProvider.EXPECT().Retrieve(gomock.Any()).Return(aws.Credentials{}, nil),
		cniClient.EXPECT().Version(ecscni.VPCENIPluginName).Return("v1", nil),
		cniClient.EXPECT().Version(ecscni.ECSBranchENIPluginName).Return("v2", nil),
		mockMobyPlugins.EXPECT().Scan().Return([]string{}, nil),
		dockerClient.EXPECT().ListPluginsWithFilters(gomock.Any(), gomock.Any(), gomock.Any(),
			gomock.Any()).Return([]string{}, nil),
		client.EXPECT().RegisterContainerInstance(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
			gomock.Any(), gomock.Any()).Do(
			func(x interface{}, attributes []types.Attribute, y interface{}, z interface{}, w interface{},
				outpostARN interface{}) {
				vpcFound := false
				subnetFound := false
				for _, attribute := range attributes {
					if aws.ToString(attribute.Name) == vpcIDAttributeName &&
						aws.ToString(attribute.Value) == vpcID {
						vpcFound = true
					}
					if aws.ToString(attribute.Name) == subnetIDAttributeName &&
						aws.ToString(attribute.Value) == subnetID {
						subnetFound = true
					}
				}
				assert.True(t, vpcFound)
				assert.True(t, subnetFound)
			}).Return("arn", "", nil),
		imageManager.EXPECT().SetDataClient(gomock.Any()),
		dockerClient.EXPECT().ContainerEvents(gomock.Any()).Return(containerChangeEvents, nil),
	)

	cfg := getTestConfig()
	cfg.TaskENIEnabled = config.BooleanDefaultFalse{Value: config.ExplicitlyEnabled}
	cfg.ENITrunkingEnabled = config.BooleanDefaultTrue{Value: config.ExplicitlyEnabled}
	ctx, cancel := context.WithCancel(context.TODO())
	// Cancel the context to cancel async routines
	agent := &ecsAgent{
		ctx:               ctx,
		cfg:               &cfg,
		credentialsCache:  aws.NewCredentialsCache(mockCredentialsProvider),
		dataClient:        data.NewNoopClient(),
		dockerClient:      dockerClient,
		pauseLoader:       mockPauseLoader,
		eniWatcher:        eniWatcher,
		cniClient:         cniClient,
		ec2MetadataClient: mockMetadata,
		terminationHandler: func(state dockerstate.TaskEngineState, dataClient data.Client, taskEngine engine.TaskEngine, cancel context.CancelFunc) {
		},
		mobyPlugins:           mockMobyPlugins,
		serviceconnectManager: mockServiceConnectManager,
	}

	getPid = func() int {
		return 10
	}
	defer resetGetpid()

	var agentW sync.WaitGroup
	agentW.Add(1)
	go func() {

		agent.doStart(eventstream.NewEventStream("events", ctx),
			credentialsManager, dockerstate.NewTaskEngineState(), imageManager, client, execCmdMgr)
		agentW.Done()
	}()

	// Wait for both DiscoverPollEndpointInput and DiscoverTelemetryEndpoint to be
	// invoked. These are used as proxies to indicate that acs and tcs handlers'
	// NewSession call has been invoked
	discoverEndpointsInvoked.Wait()
	cancel()
	agentW.Wait()
}

func TestSetVPCSubnetHappyPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockMetadata := mock_ec2.NewMockEC2MetadataClient(ctrl)
	gomock.InOrder(
		mockMetadata.EXPECT().PrimaryENIMAC().Return(mac, nil),
		mockMetadata.EXPECT().VPCID(mac).Return(vpcID, nil),
		mockMetadata.EXPECT().SubnetID(mac).Return(subnetID, nil),
	)

	agent := &ecsAgent{ec2MetadataClient: mockMetadata}
	err, ok := agent.setVPCSubnet()
	assert.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, vpcID, agent.vpc)
	assert.Equal(t, subnetID, agent.subnet)
}

func TestSetVPCSubnetClassicEC2(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockMetadata := mock_ec2.NewMockEC2MetadataClient(ctrl)
	gomock.InOrder(
		mockMetadata.EXPECT().PrimaryENIMAC().Return(mac, nil),
		mockMetadata.EXPECT().VPCID(mac).Return("", &smithy.GenericAPIError{
			Code:    "EC2MetadataError",
			Message: "failed to make EC2Metadata request",
		}),
	)
	agent := &ecsAgent{ec2MetadataClient: mockMetadata}
	err, ok := agent.setVPCSubnet()
	assert.Error(t, err)
	assert.Equal(t, instanceNotLaunchedInVPCError, err)
	assert.False(t, ok)
	assert.Equal(t, "", agent.vpc)
	assert.Equal(t, "", agent.subnet)
}

func TestSetVPCSubnetPrimaryENIMACError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockMetadata := mock_ec2.NewMockEC2MetadataClient(ctrl)
	mockMetadata.EXPECT().PrimaryENIMAC().Return("", errors.New("error"))
	agent := &ecsAgent{ec2MetadataClient: mockMetadata}
	err, ok := agent.setVPCSubnet()
	assert.Error(t, err)
	assert.False(t, ok)
	assert.Equal(t, "", agent.vpc)
	assert.Equal(t, "", agent.subnet)
}

func TestSetVPCSubnetVPCIDError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockMetadata := mock_ec2.NewMockEC2MetadataClient(ctrl)
	gomock.InOrder(
		mockMetadata.EXPECT().PrimaryENIMAC().Return(mac, nil),
		mockMetadata.EXPECT().VPCID(mac).Return("", errors.New("error")),
	)
	agent := &ecsAgent{ec2MetadataClient: mockMetadata}
	err, ok := agent.setVPCSubnet()
	assert.Error(t, err)
	assert.True(t, ok)
	assert.Equal(t, "", agent.vpc)
	assert.Equal(t, "", agent.subnet)
}

func TestSetVPCSubnetSubnetIDError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockMetadata := mock_ec2.NewMockEC2MetadataClient(ctrl)
	gomock.InOrder(
		mockMetadata.EXPECT().PrimaryENIMAC().Return(mac, nil),
		mockMetadata.EXPECT().VPCID(mac).Return(vpcID, nil),
		mockMetadata.EXPECT().SubnetID(mac).Return("", errors.New("error")),
	)
	agent := &ecsAgent{ec2MetadataClient: mockMetadata}
	err, ok := agent.setVPCSubnet()
	assert.Error(t, err)
	assert.False(t, ok)
	assert.Equal(t, "", agent.vpc)
	assert.Equal(t, "", agent.subnet)
}

func TestQueryCNIPluginsCapabilitiesHappyPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cniCapabilities := []string{ecscni.CapabilityAWSVPCNetworkingMode}
	cniClient := mock_ecscni.NewMockCNIClient(ctrl)
	gomock.InOrder(
		cniClient.EXPECT().Capabilities(ecscni.VPCENIPluginName).Return(cniCapabilities, nil),
		cniClient.EXPECT().Capabilities(ecscni.ECSBridgePluginName).Return(cniCapabilities, nil),
		cniClient.EXPECT().Capabilities(ecscni.ECSIPAMPluginName).Return(cniCapabilities, nil),
		cniClient.EXPECT().Capabilities(ecscni.ECSAppMeshPluginName).Return(cniCapabilities, nil),
		cniClient.EXPECT().Capabilities(ecscni.ECSBranchENIPluginName).Return(cniCapabilities, nil),
	)
	agent := &ecsAgent{
		cniClient: cniClient,
		cfg: &config.Config{
			ENITrunkingEnabled: config.BooleanDefaultTrue{Value: config.ExplicitlyEnabled},
		},
	}
	assert.NoError(t, agent.verifyCNIPluginsCapabilities())
}

func TestQueryCNIPluginsCapabilitiesEmptyCapabilityListFromPlugin(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cniClient := mock_ecscni.NewMockCNIClient(ctrl)
	cniClient.EXPECT().Capabilities(ecscni.VPCENIPluginName).Return([]string{}, nil)

	agent := &ecsAgent{
		cniClient: cniClient,
	}

	assert.Error(t, agent.verifyCNIPluginsCapabilities())
}

func TestQueryCNIPluginsCapabilitiesMissAppMesh(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cniCapabilities := []string{ecscni.CapabilityAWSVPCNetworkingMode}
	cniClient := mock_ecscni.NewMockCNIClient(ctrl)
	gomock.InOrder(
		cniClient.EXPECT().Capabilities(ecscni.VPCENIPluginName).Return(cniCapabilities, nil),
		cniClient.EXPECT().Capabilities(ecscni.ECSBridgePluginName).Return(cniCapabilities, nil),
		cniClient.EXPECT().Capabilities(ecscni.ECSIPAMPluginName).Return(cniCapabilities, nil),
		cniClient.EXPECT().Capabilities(ecscni.ECSAppMeshPluginName).Return(nil, errors.New("error")),
	)
	cfg := getTestConfig()
	agent := &ecsAgent{
		cniClient: cniClient,
		cfg:       &cfg,
	}
	assert.Error(t, agent.verifyCNIPluginsCapabilities())
}

func TestQueryCNIPluginsCapabilitiesErrorGettingCapabilitiesFromPlugin(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cniClient := mock_ecscni.NewMockCNIClient(ctrl)
	cniClient.EXPECT().Capabilities(ecscni.VPCENIPluginName).Return(nil, errors.New("error"))

	agent := &ecsAgent{
		cniClient: cniClient,
	}

	assert.Error(t, agent.verifyCNIPluginsCapabilities())
}

func setupMocksForInitializeTaskENIDependencies(t *testing.T) (*gomock.Controller,
	*mock_dockerstate.MockTaskEngineState,
	*mock_engine.MockTaskEngine) {
	ctrl := gomock.NewController(t)

	return ctrl,
		mock_dockerstate.NewMockTaskEngineState(ctrl),
		mock_engine.NewMockTaskEngine(ctrl)
}

func TestInitializeTaskENIDependenciesNoInit(t *testing.T) {
	ctrl, state, taskEngine := setupMocksForInitializeTaskENIDependencies(t)
	defer ctrl.Finish()

	agent := &ecsAgent{}

	getPid = func() int {
		return 1
	}
	defer resetGetpid()

	err, ok := agent.initializeTaskENIDependencies(state, taskEngine)
	assert.Error(t, err)
	assert.True(t, ok)
}

func TestInitializeTaskENIDependenciesSetVPCSubnetError(t *testing.T) {
	ctrl, state, taskEngine := setupMocksForInitializeTaskENIDependencies(t)
	defer ctrl.Finish()

	mockMetadata := mock_ec2.NewMockEC2MetadataClient(ctrl)
	gomock.InOrder(
		mockMetadata.EXPECT().PrimaryENIMAC().Return("", errors.New("error")),
	)
	agent := &ecsAgent{
		ec2MetadataClient: mockMetadata,
	}
	getPid = func() int {
		return 10
	}
	defer resetGetpid()

	err, ok := agent.initializeTaskENIDependencies(state, taskEngine)
	assert.Error(t, err)
	assert.False(t, ok)
}

func TestInitializeTaskENIDependenciesQueryCNICapabilitiesError(t *testing.T) {
	ctrl, state, taskEngine := setupMocksForInitializeTaskENIDependencies(t)
	defer ctrl.Finish()

	mockMetadata := mock_ec2.NewMockEC2MetadataClient(ctrl)
	cniClient := mock_ecscni.NewMockCNIClient(ctrl)
	gomock.InOrder(
		mockMetadata.EXPECT().PrimaryENIMAC().Return(mac, nil),
		mockMetadata.EXPECT().VPCID(mac).Return(vpcID, nil),
		mockMetadata.EXPECT().SubnetID(mac).Return(subnetID, nil),
		cniClient.EXPECT().Capabilities(ecscni.VPCENIPluginName).Return([]string{}, nil),
	)
	agent := &ecsAgent{
		ec2MetadataClient: mockMetadata,
		cniClient:         cniClient,
	}

	getPid = func() int {
		return 10
	}
	defer resetGetpid()

	err, ok := agent.initializeTaskENIDependencies(state, taskEngine)
	assert.Error(t, err)
	assert.True(t, ok)
}

// TODO: At some point in the future, enisetup.New() will be refactored to be
// platform independent and we would be able to wrap it in a factory interface
// so that we can mock the factory and test the initialization code path for
// cases where udev monitor initialization fails as well

func TestDoStartCgroupInitHappyPath(t *testing.T) {
	ctrl, credentialsManager, state, imageManager, client,
		dockerClient, _, _, execCmdMgr, _ := setup(t)
	defer ctrl.Finish()
	mockCredentialsProvider := app_mocks.NewMockCredentialsProvider(ctrl)
	mockControl := mock_control.NewMockControl(ctrl)
	mockMobyPlugins := mock_mobypkgwrapper.NewMockPlugins(ctrl)
	mockPauseLoader := mock_loader.NewMockLoader(ctrl)
	var discoverEndpointsInvoked sync.WaitGroup
	discoverEndpointsInvoked.Add(2)
	containerChangeEvents := make(chan dockerapi.DockerContainerChangeEvent)

	ec2MetadataClient := mock_ec2.NewMockEC2MetadataClient(ctrl)
	dockerClient.EXPECT().Version(gomock.Any(), gomock.Any()).AnyTimes()
	dockerClient.EXPECT().SupportedVersions().Return(apiVersions).AnyTimes()
	imageManager.EXPECT().StartImageCleanupProcess(gomock.Any()).MaxTimes(1)
	ec2MetadataClient.EXPECT().PrimaryENIMAC().Return("mac", nil)
	ec2MetadataClient.EXPECT().VPCID(gomock.Eq("mac")).Return("vpc-id", nil)
	ec2MetadataClient.EXPECT().SubnetID(gomock.Eq("mac")).Return("subnet-id", nil)
	ec2MetadataClient.EXPECT().OutpostARN().Return("", nil)
	mockPauseLoader.EXPECT().LoadImage(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	mockPauseLoader.EXPECT().IsLoaded(gomock.Any()).Return(true, nil).AnyTimes()
	mockServiceConnectManager := mock_serviceconnect.NewMockManager(ctrl)
	mockServiceConnectManager.EXPECT().IsLoaded(gomock.Any()).Return(true, nil).AnyTimes()
	mockServiceConnectManager.EXPECT().GetLoadedAppnetVersion().AnyTimes()
	mockServiceConnectManager.EXPECT().GetCapabilitiesForAppnetInterfaceVersion("").AnyTimes()
	mockServiceConnectManager.EXPECT().SetECSClient(gomock.Any(), gomock.Any()).AnyTimes()
	mockServiceConnectManager.EXPECT().GetAppnetContainerTarballDir().AnyTimes()
	mockServiceConnectManager.EXPECT().GetLoadedImageName().Return("service_connect_agent:v1").AnyTimes()

	originalImportAll := md.ImportAll
	defer func() { md.ImportAll = originalImportAll }()
	md.ImportAll = func() ([]*md.ManagedDaemon, error) {
		return []*md.ManagedDaemon{}, nil
	}

	imageManager.EXPECT().AddImageToCleanUpExclusionList(gomock.Eq("service_connect_agent:v1")).Times(1)
	client.EXPECT().GetHostResources().Return(testHostResource, nil).Times(1)

	gomock.InOrder(
		mockControl.EXPECT().Init().Return(nil),
		mockCredentialsProvider.EXPECT().Retrieve(gomock.Any()).Return(aws.Credentials{}, nil),
		mockMobyPlugins.EXPECT().Scan().Return([]string{}, nil),
		dockerClient.EXPECT().ListPluginsWithFilters(gomock.Any(), gomock.Any(), gomock.Any(),
			gomock.Any()).Return([]string{}, nil),
		client.EXPECT().RegisterContainerInstance(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
			gomock.Any(), gomock.Any()).Return("arn", "", nil),
		imageManager.EXPECT().SetDataClient(gomock.Any()),
		dockerClient.EXPECT().ContainerEvents(gomock.Any()).Return(containerChangeEvents, nil),
		state.EXPECT().AllImageStates().Return(nil),
		state.EXPECT().AllENIAttachments().Return(nil),
		state.EXPECT().AllTasks().Return(nil),
		state.EXPECT().AllTasks().Return(nil),
		client.EXPECT().DiscoverPollEndpoint(gomock.Any()).Do(func(x interface{}) {
			// Ensures that the test waits until acs session has bee started
			discoverEndpointsInvoked.Done()
		}).Return("poll-endpoint", nil),
		client.EXPECT().DiscoverPollEndpoint(gomock.Any()).Return("acs-endpoint", nil).AnyTimes(),
		client.EXPECT().DiscoverTelemetryEndpoint(gomock.Any()).Do(func(x interface{}) {
			// Ensures that the test waits until telemetry session has been started
			discoverEndpointsInvoked.Done()
		}).Return("telemetry-endpoint", nil),
		client.EXPECT().DiscoverTelemetryEndpoint(gomock.Any()).Return(
			"tele-endpoint", nil).AnyTimes(),
		dockerClient.EXPECT().ListContainers(gomock.Any(), gomock.Any(), gomock.Any()).Return(
			dockerapi.ListContainersResponse{}).AnyTimes(),
	)

	cfg := config.DefaultConfig(ipcompatibility.NewIPv4OnlyCompatibility())
	ctx, cancel := context.WithCancel(context.TODO())
	// Cancel the context to cancel async routines
	agent := &ecsAgent{
		ctx:              ctx,
		cfg:              &cfg,
		credentialsCache: aws.NewCredentialsCache(mockCredentialsProvider),
		pauseLoader:      mockPauseLoader,
		dockerClient:     dockerClient,
		terminationHandler: func(state dockerstate.TaskEngineState, dataClient data.Client, taskEngine engine.TaskEngine, cancel context.CancelFunc) {
		},
		mobyPlugins:       mockMobyPlugins,
		ec2MetadataClient: ec2MetadataClient,
		resourceFields: &taskresource.ResourceFields{
			Control: mockControl,
		},
		serviceconnectManager: mockServiceConnectManager,
	}

	var agentW sync.WaitGroup
	agentW.Add(1)
	go func() {
		agent.doStart(eventstream.NewEventStream("events", ctx),
			credentialsManager, state, imageManager, client, execCmdMgr)
		agentW.Done()
	}()

	// Wait for both DiscoverPollEndpointInput and DiscoverTelemetryEndpoint to be
	// invoked. These are used as proxies to indicate that acs and tcs handlers'
	// NewSession call has been invoked

	discoverEndpointsInvoked.Wait()
	cancel()
	agentW.Wait()
}

func TestDoStartCgroupInitErrorPath(t *testing.T) {
	ctrl, credentialsManager, state, imageManager, client,
		dockerClient, _, _, execCmdMgr, _ := setup(t)
	defer ctrl.Finish()

	mockCredentialsProvider := app_mocks.NewMockCredentialsProvider(ctrl)
	mockControl := mock_control.NewMockControl(ctrl)
	mockPauseLoader := mock_loader.NewMockLoader(ctrl)
	var discoverEndpointsInvoked sync.WaitGroup
	discoverEndpointsInvoked.Add(2)

	dockerClient.EXPECT().Version(gomock.Any(), gomock.Any()).AnyTimes()
	dockerClient.EXPECT().SupportedVersions().Return(apiVersions).AnyTimes()
	imageManager.EXPECT().StartImageCleanupProcess(gomock.Any()).MaxTimes(1)
	mockPauseLoader.EXPECT().LoadImage(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	mockPauseLoader.EXPECT().IsLoaded(gomock.Any()).Return(true, nil).AnyTimes()
	mockServiceConnectManager := mock_serviceconnect.NewMockManager(ctrl)
	mockServiceConnectManager.EXPECT().IsLoaded(gomock.Any()).Return(true, nil).AnyTimes()
	mockServiceConnectManager.EXPECT().GetLoadedAppnetVersion().AnyTimes()
	mockServiceConnectManager.EXPECT().GetCapabilitiesForAppnetInterfaceVersion("").AnyTimes()
	mockServiceConnectManager.EXPECT().SetECSClient(gomock.Any(), gomock.Any()).AnyTimes()

	originalImportAll := md.ImportAll
	defer func() { md.ImportAll = originalImportAll }()
	md.ImportAll = func() ([]*md.ManagedDaemon, error) {
		return []*md.ManagedDaemon{}, nil
	}

	mockControl.EXPECT().Init().Return(errors.New("test error"))

	cfg := getTestConfig()
	cfg.TaskCPUMemLimit.Value = config.ExplicitlyEnabled

	ctx, cancel := context.WithCancel(context.TODO())
	// Cancel the context to cancel async routines
	defer cancel()
	agent := &ecsAgent{
		ctx:              ctx,
		cfg:              &cfg,
		credentialsCache: aws.NewCredentialsCache(mockCredentialsProvider),
		dockerClient:     dockerClient,
		pauseLoader:      mockPauseLoader,
		terminationHandler: func(state dockerstate.TaskEngineState, dataClient data.Client, taskEngine engine.TaskEngine, cancel context.CancelFunc) {
		},
		resourceFields: &taskresource.ResourceFields{
			Control: mockControl,
		},
		serviceconnectManager: mockServiceConnectManager,
	}

	status := agent.doStart(eventstream.NewEventStream("events", ctx),
		credentialsManager, state, imageManager, client, execCmdMgr)

	assert.Equal(t, exitcodes.ExitTerminal, status)
}

func TestDoStartGPUManagerHappyPath(t *testing.T) {
	ctrl, credentialsManager, state, imageManager, client,
		dockerClient, _, _, execCmdMgr, _ := setup(t)
	defer ctrl.Finish()
	mockCredentialsProvider := app_mocks.NewMockCredentialsProvider(ctrl)
	mockGPUManager := mock_gpu.NewMockGPUManager(ctrl)
	mockMobyPlugins := mock_mobypkgwrapper.NewMockPlugins(ctrl)
	ec2MetadataClient := mock_ec2.NewMockEC2MetadataClient(ctrl)
	mockPauseLoader := mock_loader.NewMockLoader(ctrl)

	devices := []types.PlatformDevice{
		{
			Id:   aws.String("id1"),
			Type: types.PlatformDeviceTypeGpu,
		},
		{
			Id:   aws.String("id2"),
			Type: types.PlatformDeviceTypeGpu,
		},
		{
			Id:   aws.String("id3"),
			Type: types.PlatformDeviceTypeGpu,
		},
	}
	var discoverEndpointsInvoked sync.WaitGroup
	discoverEndpointsInvoked.Add(2)
	containerChangeEvents := make(chan dockerapi.DockerContainerChangeEvent)

	dockerClient.EXPECT().Version(gomock.Any(), gomock.Any()).AnyTimes()
	dockerClient.EXPECT().SupportedVersions().Return(apiVersions).AnyTimes()
	imageManager.EXPECT().StartImageCleanupProcess(gomock.Any()).MaxTimes(1)
	ec2MetadataClient.EXPECT().PrimaryENIMAC().Return("mac", nil)
	ec2MetadataClient.EXPECT().VPCID(gomock.Eq("mac")).Return("vpc-id", nil)
	ec2MetadataClient.EXPECT().SubnetID(gomock.Eq("mac")).Return("subnet-id", nil)
	ec2MetadataClient.EXPECT().OutpostARN().Return("", nil)
	mockPauseLoader.EXPECT().LoadImage(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	mockPauseLoader.EXPECT().IsLoaded(gomock.Any()).Return(true, nil).AnyTimes()
	mockServiceConnectManager := mock_serviceconnect.NewMockManager(ctrl)
	mockServiceConnectManager.EXPECT().IsLoaded(gomock.Any()).Return(true, nil).AnyTimes()
	mockServiceConnectManager.EXPECT().GetLoadedAppnetVersion().AnyTimes()
	mockServiceConnectManager.EXPECT().GetCapabilitiesForAppnetInterfaceVersion("").AnyTimes()
	mockServiceConnectManager.EXPECT().SetECSClient(gomock.Any(), gomock.Any()).AnyTimes()
	mockServiceConnectManager.EXPECT().GetAppnetContainerTarballDir().AnyTimes()
	mockServiceConnectManager.EXPECT().GetLoadedImageName().Return("service_connect_agent:v1").AnyTimes()

	originalImportAll := md.ImportAll
	defer func() { md.ImportAll = originalImportAll }()
	md.ImportAll = func() ([]*md.ManagedDaemon, error) {
		return []*md.ManagedDaemon{}, nil
	}

	imageManager.EXPECT().AddImageToCleanUpExclusionList(gomock.Eq("service_connect_agent:v1")).Times(1)
	client.EXPECT().GetHostResources().Return(testHostResource, nil).Times(1)
	mockGPUManager.EXPECT().GetDevices().Return(devices).AnyTimes()

	gomock.InOrder(
		mockGPUManager.EXPECT().Initialize().Return(nil),
		mockCredentialsProvider.EXPECT().Retrieve(gomock.Any()).Return(aws.Credentials{}, nil),
		mockMobyPlugins.EXPECT().Scan().Return([]string{}, nil),
		dockerClient.EXPECT().ListPluginsWithFilters(gomock.Any(), gomock.Any(), gomock.Any(),
			gomock.Any()).Return([]string{}, nil),
		mockGPUManager.EXPECT().GetDriverVersion().Return("396.44"),
		mockGPUManager.EXPECT().GetDevices().Return(devices).AnyTimes(),
		client.EXPECT().RegisterContainerInstance(gomock.Any(), gomock.Any(), gomock.Any(),
			gomock.Any(), devices, gomock.Any()).Return("arn", "", nil),
		imageManager.EXPECT().SetDataClient(gomock.Any()),
		dockerClient.EXPECT().ContainerEvents(gomock.Any()).Return(containerChangeEvents, nil),
		state.EXPECT().AllImageStates().Return(nil),
		state.EXPECT().AllENIAttachments().Return(nil),
		state.EXPECT().AllTasks().Return(nil),
		state.EXPECT().AllTasks().Return(nil),
		client.EXPECT().DiscoverPollEndpoint(gomock.Any()).Do(func(x interface{}) {
			// Ensures that the test waits until acs session has been started
			discoverEndpointsInvoked.Done()
		}).Return("poll-endpoint", nil),
		client.EXPECT().DiscoverPollEndpoint(gomock.Any()).Return("acs-endpoint", nil).AnyTimes(),
		client.EXPECT().DiscoverTelemetryEndpoint(gomock.Any()).Do(func(x interface{}) {
			// Ensures that the test waits until telemetry session has been started
			discoverEndpointsInvoked.Done()
		}).Return("telemetry-endpoint", nil),
		client.EXPECT().DiscoverTelemetryEndpoint(gomock.Any()).Return(
			"tele-endpoint", nil).AnyTimes(),
		dockerClient.EXPECT().ListContainers(gomock.Any(), gomock.Any(), gomock.Any()).Return(
			dockerapi.ListContainersResponse{}).AnyTimes(),
	)

	cfg := getTestConfig()
	cfg.GPUSupportEnabled = true
	ctx, cancel := context.WithCancel(context.TODO())
	// Cancel the context to cancel async routines
	agent := &ecsAgent{
		ctx:              ctx,
		cfg:              &cfg,
		credentialsCache: aws.NewCredentialsCache(mockCredentialsProvider),
		dockerClient:     dockerClient,
		pauseLoader:      mockPauseLoader,
		terminationHandler: func(state dockerstate.TaskEngineState, dataClient data.Client, taskEngine engine.TaskEngine, cancel context.CancelFunc) {
		},
		mobyPlugins:       mockMobyPlugins,
		ec2MetadataClient: ec2MetadataClient,
		resourceFields: &taskresource.ResourceFields{
			NvidiaGPUManager: mockGPUManager,
		},
		serviceconnectManager: mockServiceConnectManager,
	}

	var agentW sync.WaitGroup
	agentW.Add(1)
	go func() {
		agent.doStart(eventstream.NewEventStream("events", ctx),
			credentialsManager, state, imageManager, client, execCmdMgr)
		agentW.Done()
	}()

	// Wait for both DiscoverPollEndpointInput and DiscoverTelemetryEndpoint to be
	// invoked. These are used as proxies to indicate that acs and tcs handlers'
	// NewSession call has been invoked

	discoverEndpointsInvoked.Wait()
	cancel()
	agentW.Wait()
}

func TestDoStartGPUManagerInitError(t *testing.T) {
	ctrl, credentialsManager, state, imageManager, client,
		dockerClient, _, _, execCmdMgr, _ := setup(t)
	defer ctrl.Finish()

	mockCredentialsProvider := app_mocks.NewMockCredentialsProvider(ctrl)
	mockGPUManager := mock_gpu.NewMockGPUManager(ctrl)
	mockPauseLoader := mock_loader.NewMockLoader(ctrl)
	var discoverEndpointsInvoked sync.WaitGroup
	discoverEndpointsInvoked.Add(2)

	dockerClient.EXPECT().Version(gomock.Any(), gomock.Any()).AnyTimes()
	dockerClient.EXPECT().SupportedVersions().Return(apiVersions).AnyTimes()
	imageManager.EXPECT().StartImageCleanupProcess(gomock.Any()).MaxTimes(1)
	mockGPUManager.EXPECT().Initialize().Return(errors.New("init error"))
	mockPauseLoader.EXPECT().LoadImage(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	mockPauseLoader.EXPECT().IsLoaded(gomock.Any()).Return(true, nil).AnyTimes()
	mockServiceConnectManager := mock_serviceconnect.NewMockManager(ctrl)
	mockServiceConnectManager.EXPECT().IsLoaded(gomock.Any()).Return(true, nil).AnyTimes()
	mockServiceConnectManager.EXPECT().GetLoadedAppnetVersion().AnyTimes()
	mockServiceConnectManager.EXPECT().GetCapabilitiesForAppnetInterfaceVersion("").AnyTimes()
	mockServiceConnectManager.EXPECT().SetECSClient(gomock.Any(), gomock.Any()).AnyTimes()
	client.EXPECT().GetHostResources().Return(testHostResource, nil).Times(1)

	cfg := getTestConfig()
	cfg.GPUSupportEnabled = true
	ctx, cancel := context.WithCancel(context.TODO())
	// Cancel the context to cancel async routines
	defer cancel()
	agent := &ecsAgent{
		ctx:              ctx,
		cfg:              &cfg,
		credentialsCache: aws.NewCredentialsCache(mockCredentialsProvider),
		dockerClient:     dockerClient,
		pauseLoader:      mockPauseLoader,
		terminationHandler: func(state dockerstate.TaskEngineState, dataClient data.Client, taskEngine engine.TaskEngine, cancel context.CancelFunc) {
		},
		resourceFields: &taskresource.ResourceFields{
			NvidiaGPUManager: mockGPUManager,
		},
		serviceconnectManager: mockServiceConnectManager,
	}

	status := agent.doStart(eventstream.NewEventStream("events", ctx),
		credentialsManager, state, imageManager, client, execCmdMgr)

	assert.Equal(t, exitcodes.ExitError, status)
}

func TestDoStartTaskENIPauseError(t *testing.T) {
	ctrl, credentialsManager, state, imageManager, client,
		dockerClient, _, _, execCmdMgr, _ := setup(t)
	defer ctrl.Finish()

	cniClient := mock_ecscni.NewMockCNIClient(ctrl)
	mockCredentialsProvider := app_mocks.NewMockCredentialsProvider(ctrl)
	mockPauseLoader := mock_loader.NewMockLoader(ctrl)
	mockMetadata := mock_ec2.NewMockEC2MetadataClient(ctrl)
	mockMobyPlugins := mock_mobypkgwrapper.NewMockPlugins(ctrl)

	var discoverEndpointsInvoked sync.WaitGroup
	discoverEndpointsInvoked.Add(2)

	// These calls are expected to happen, but cannot be ordered as they are
	// invoked via go routines, which will lead to occasional test failures
	dockerClient.EXPECT().Version(gomock.Any(), gomock.Any()).AnyTimes()
	dockerClient.EXPECT().SupportedVersions().Return(apiVersions).AnyTimes()
	dockerClient.EXPECT().ListContainers(gomock.Any(), gomock.Any(), gomock.Any()).Return(
		dockerapi.ListContainersResponse{}).AnyTimes()
	imageManager.EXPECT().StartImageCleanupProcess(gomock.Any()).MaxTimes(1)
	mockPauseLoader.EXPECT().LoadImage(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errors.New("error")).AnyTimes()
	client.EXPECT().GetHostResources().Return(testHostResource, nil).Times(1)

	cfg := getTestConfig()
	cfg.TaskENIEnabled = config.BooleanDefaultFalse{Value: config.ExplicitlyEnabled}
	cfg.ENITrunkingEnabled = config.BooleanDefaultTrue{Value: config.ExplicitlyEnabled}
	ctx, _ := context.WithCancel(context.TODO())
	agent := &ecsAgent{
		ctx:               ctx,
		cfg:               &cfg,
		credentialsCache:  aws.NewCredentialsCache(mockCredentialsProvider),
		dockerClient:      dockerClient,
		pauseLoader:       mockPauseLoader,
		cniClient:         cniClient,
		ec2MetadataClient: mockMetadata,
		terminationHandler: func(state dockerstate.TaskEngineState, dataClient data.Client, taskEngine engine.TaskEngine, cancel context.CancelFunc) {
		},
		mobyPlugins: mockMobyPlugins,
	}

	status := agent.doStart(eventstream.NewEventStream("events", ctx),
		credentialsManager, state, imageManager, client, execCmdMgr)

	assert.Equal(t, exitcodes.ExitError, status)
}
