//go:build windows && unit
// +build windows,unit

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

package fsxwindowsfileserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/aws/amazon-ecs-agent/agent/config"
	"github.com/aws/amazon-ecs-agent/agent/utils"
	"github.com/aws/amazon-ecs-agent/ecs-agent/ipcompatibility"

	mock_asm_factory "github.com/aws/amazon-ecs-agent/agent/asm/factory/mocks"
	mock_secretsmanageriface "github.com/aws/amazon-ecs-agent/agent/asm/mocks"
	mock_fsx_factory "github.com/aws/amazon-ecs-agent/agent/fsx/factory/mocks"
	mock_fsxiface "github.com/aws/amazon-ecs-agent/agent/fsx/mocks"
	mock_ssm_factory "github.com/aws/amazon-ecs-agent/agent/ssm/factory/mocks"
	mock_ssmiface "github.com/aws/amazon-ecs-agent/agent/ssm/mocks"
	"github.com/aws/amazon-ecs-agent/agent/taskresource"
	resourcestatus "github.com/aws/amazon-ecs-agent/agent/taskresource/status"
	apitaskstatus "github.com/aws/amazon-ecs-agent/ecs-agent/api/task/status"
	"github.com/aws/amazon-ecs-agent/ecs-agent/credentials"
	mock_credentials "github.com/aws/amazon-ecs-agent/ecs-agent/credentials/mocks"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/fsx"
	"github.com/aws/aws-sdk-go-v2/service/fsx/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	taskARN                = "arn:aws:ecs:us-west-2:123456789012:task/12345-678901234-56789"
	executionCredentialsID = "exec-creds-id"
	fileSystemId           = "fs-12345678"
	rootDirectory          = `\test\directory`
	credentialsParameter   = "arn"
	domain                 = "testdomain"
	hostPath               = `Z:\`
)

var testConfig = &config.Config{
	InstanceIPCompatibility: ipcompatibility.NewIPCompatibility(true, true),
}

func setup(t *testing.T) (
	*FSxWindowsFileServerResource, *mock_credentials.MockManager, *mock_ssm_factory.MockSSMClientCreator,
	*mock_asm_factory.MockClientCreator, *mock_fsx_factory.MockFSxClientCreator, *mock_ssmiface.MockSSMClient,
	*mock_secretsmanageriface.MockSecretsManagerAPI, *mock_fsxiface.MockFSxClient) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	credentialsManager := mock_credentials.NewMockManager(ctrl)
	ssmClientCreator := mock_ssm_factory.NewMockSSMClientCreator(ctrl)
	asmClientCreator := mock_asm_factory.NewMockClientCreator(ctrl)
	fsxClientCreator := mock_fsx_factory.NewMockFSxClientCreator(ctrl)

	mockSSMClient := mock_ssmiface.NewMockSSMClient(ctrl)
	mockASMClient := mock_secretsmanageriface.NewMockSecretsManagerAPI(ctrl)
	mockFSxClient := mock_fsxiface.NewMockFSxClient(ctrl)

	fv := &FSxWindowsFileServerResource{
		knownStatusUnsafe:   resourcestatus.ResourceCreated,
		desiredStatusUnsafe: resourcestatus.ResourceCreated,
		taskARN:             taskARN,
	}
	fv.Initialize(
		testConfig,
		&taskresource.ResourceFields{
			ResourceFieldsCommon: &taskresource.ResourceFieldsCommon{
				SSMClientCreator:   ssmClientCreator,
				ASMClientCreator:   asmClientCreator,
				FSxClientCreator:   fsxClientCreator,
				CredentialsManager: credentialsManager,
			},
		}, apitaskstatus.TaskStatusNone, apitaskstatus.TaskRunning)
	return fv, credentialsManager, ssmClientCreator, asmClientCreator, fsxClientCreator, mockSSMClient, mockASMClient, mockFSxClient
}

func TestInitialize(t *testing.T) {
	fv, _, _, _, _, _, _, _ := setup(t)
	assert.NotNil(t, fv.credentialsManager)
	assert.NotNil(t, fv.ssmClientCreator)
	assert.NotNil(t, fv.asmClientCreator)
	assert.NotNil(t, fv.fsxClientCreator)
	assert.NotNil(t, fv.statusToTransitions)
	assert.NotNil(t, fv.ipCompatibility)
}

func TestMarshalUnmarshalJSON(t *testing.T) {
	volumeConfig := FSxWindowsFileServerVolumeConfig{
		FileSystemID:  "fs-12345678",
		RootDirectory: rootDirectory,
		AuthConfig: FSxWindowsFileServerAuthConfig{
			CredentialsParameter: credentialsParameter,
			Domain:               domain,
		},
		HostPath: hostPath,
	}
	fsxWindowsFileServerIn := &FSxWindowsFileServerResource{
		Name:                   "test",
		VolumeConfig:           volumeConfig,
		taskARN:                taskARN,
		executionCredentialsID: executionCredentialsID,
		createdAtUnsafe:        time.Time{},
		knownStatusUnsafe:      resourcestatus.ResourceCreated,
		desiredStatusUnsafe:    resourcestatus.ResourceCreated,
	}

	bytes, err := json.Marshal(fsxWindowsFileServerIn)
	require.NoError(t, err)

	fsxWindowsFileServerOut := &FSxWindowsFileServerResource{}
	err = json.Unmarshal(bytes, fsxWindowsFileServerOut)
	require.NoError(t, err)
	assert.Equal(t, fsxWindowsFileServerIn.Name, fsxWindowsFileServerOut.Name)
	assert.Equal(t, fsxWindowsFileServerIn.VolumeConfig, fsxWindowsFileServerOut.VolumeConfig)
	assert.Equal(t, fsxWindowsFileServerIn.taskARN, fsxWindowsFileServerOut.taskARN)
	assert.Equal(t, fsxWindowsFileServerIn.executionCredentialsID, fsxWindowsFileServerOut.executionCredentialsID)
	assert.WithinDuration(t, fsxWindowsFileServerIn.createdAtUnsafe, fsxWindowsFileServerOut.createdAtUnsafe, time.Microsecond)
	assert.Equal(t, fsxWindowsFileServerIn.desiredStatusUnsafe, fsxWindowsFileServerOut.desiredStatusUnsafe)
	assert.Equal(t, fsxWindowsFileServerIn.knownStatusUnsafe, fsxWindowsFileServerOut.knownStatusUnsafe)
}

// TODO: Make tests table driven
func TestRetrieveCredentials(t *testing.T) {
	fv, _, ssmClientCreator, _, _, mockSSMClient, _, _ := setup(t)

	credentialsParameterARN := "arn:aws:ssm:us-west-2:123456789012:parameter/test"

	ssmTestData := "{\n\"username\": \"user\", \n\"password\": \"pass\"\n}"
	ssmClientOutput := &ssm.GetParametersOutput{
		InvalidParameters: []string{},
		Parameters: []ssmtypes.Parameter{
			ssmtypes.Parameter{
				Name:  aws.String("/test"),
				Value: aws.String(ssmTestData),
			},
		},
	}

	iamCredentials := credentials.IAMRoleCredentials{
		CredentialsID: "test-cred-id",
	}

	gomock.InOrder(
		ssmClientCreator.EXPECT().NewSSMClient(gomock.Any(), gomock.Any(), testConfig.InstanceIPCompatibility).Return(mockSSMClient, nil),
		mockSSMClient.EXPECT().GetParameters(gomock.Any(), gomock.Any()).Return(ssmClientOutput, nil).Times(1),
	)

	err := fv.retrieveCredentials(credentialsParameterARN, iamCredentials)
	assert.NoError(t, err)

	credentials := fv.Credentials
	assert.Equal(t, "user", credentials.Username)
	assert.Equal(t, "pass", credentials.Password)
}

func TestRetrieveSSMCredentials(t *testing.T) {
	cases := []struct {
		Name                     string
		CredentialsParameterARN  string
		CredentialsParameterName string
	}{
		{
			Name:                     "TestRetrieveSSMCredentialsSimple",
			CredentialsParameterARN:  "arn:aws:ssm:us-west-2:123456789012:parameter/hello",
			CredentialsParameterName: "/hello",
		},
		{
			Name:                     "TestRetrieveSSMCredentialsPath",
			CredentialsParameterARN:  "arn:aws:ssm:us-west-2:123456789012:parameter/path1/path2/hello",
			CredentialsParameterName: "/path1/path2/hello",
		},
		{
			Name:                     "TestRetrieveSSMCredentialsSimpleWithParameter",
			CredentialsParameterARN:  "arn:aws:ssm:us-east-2:958991572715:parameter/parameter",
			CredentialsParameterName: "/parameter",
		},
		{
			Name:                     "TestRetrieveSSMCredentialsPathWithParameter",
			CredentialsParameterARN:  "arn:aws:ssm:us-east-2:958991572715:parameter/path1/path2/parameter",
			CredentialsParameterName: "/path1/path2/parameter",
		},
		{
			Name:                     "TestRetrieveSSMCredentialsPathWithParameter2",
			CredentialsParameterARN:  "arn:aws:ssm:us-east-2:958991572715:parameter/path1/parameter/hello",
			CredentialsParameterName: "/path1/parameter/hello",
		},
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			fv, _, ssmClientCreator, _, _, mockSSMClient, _, _ := setup(t)
			credentialsParameterARN := tc.CredentialsParameterARN

			ssmTestData := "{\n\"username\": \"user\", \n\"password\": \"pass\"\n}"
			ssmClientOutput := &ssm.GetParametersOutput{
				InvalidParameters: []string{},
				Parameters: []ssmtypes.Parameter{
					ssmtypes.Parameter{
						Name:  aws.String(tc.CredentialsParameterName),
						Value: aws.String(ssmTestData),
					},
				},
			}

			iamCredentials := credentials.IAMRoleCredentials{
				CredentialsID: "test-cred-id",
			}

			gomock.InOrder(
				ssmClientCreator.EXPECT().NewSSMClient(gomock.Any(), gomock.Any(), testConfig.InstanceIPCompatibility).Return(mockSSMClient, nil),
				mockSSMClient.EXPECT().GetParameters(gomock.Any(), &ssm.GetParametersInput{
					Names:          []string{tc.CredentialsParameterName},
					WithDecryption: aws.Bool(false),
				}).Return(ssmClientOutput, nil).Times(1),
			)

			err := fv.retrieveSSMCredentials(credentialsParameterARN, iamCredentials)
			assert.NoError(t, err)

			credentials := fv.Credentials
			assert.Equal(t, "user", credentials.Username)
			assert.Equal(t, "pass", credentials.Password)
		})
	}

}

func TestRetrieveASMCredentials(t *testing.T) {
	fv, _, _, asmClientCreator, _, _, mockASMClient, _ := setup(t)
	credentialsParameterARN := "arn:aws:secretsmanager:us-east-1:123456789012:secret:testing/some-random-name"

	asmTestData := "{\"username\":\"user\",\"password\":\"pass\"}"
	asmClientOutput := &secretsmanager.GetSecretValueOutput{
		SecretString: aws.String(asmTestData),
	}

	gomock.InOrder(
		asmClientCreator.EXPECT().NewASMClient(gomock.Any(), gomock.Any()).Return(mockASMClient, nil),
		mockASMClient.EXPECT().GetSecretValue(
			gomock.Any(),
			gomock.Any(),
			gomock.Any(),
		).Do(func(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) {
			assert.Equal(t, aws.ToString(in.SecretId), credentialsParameterARN)
		}).Return(asmClientOutput, nil),
	)

	iamCredentials := credentials.IAMRoleCredentials{
		CredentialsID: "test-cred-id",
	}

	err := fv.retrieveASMCredentials(credentialsParameterARN, iamCredentials)
	assert.NoError(t, err)

	credentials := fv.Credentials
	assert.Equal(t, "user", credentials.Username)
	assert.Equal(t, "pass", credentials.Password)
}

func TestRetrieveCredentialsInvalidService(t *testing.T) {
	iamCredentials := credentials.IAMRoleCredentials{
		CredentialsID: "test-cred-id",
	}

	credentialsParameterARN := "arn:aws:foo:us-east-1:123456789012:parameter/test"

	var termReason string
	fv := &FSxWindowsFileServerResource{
		terminalReason: termReason,
	}

	err := fv.retrieveCredentials(credentialsParameterARN, iamCredentials)
	assert.Error(t, err)
}

func TestRetrieveSSMCredentialsARNParseErr(t *testing.T) {
	iamCredentials := credentials.IAMRoleCredentials{
		CredentialsID: "test-cred-id",
	}

	credentialsParameterARN := "arn:aws:ssm:parameter/test"

	var termReason string
	fv := &FSxWindowsFileServerResource{
		terminalReason: termReason,
	}

	err := fv.retrieveSSMCredentials(credentialsParameterARN, iamCredentials)
	assert.Error(t, err)
}

func TestRetrieveASMCredentialsARNParseErr(t *testing.T) {
	iamCredentials := credentials.IAMRoleCredentials{
		CredentialsID: "test-cred-id",
	}

	credentialsParameterARN := "arn:aws:secretsmanager:some-random-name"

	var termReason string
	fv := &FSxWindowsFileServerResource{
		terminalReason: termReason,
	}

	err := fv.retrieveASMCredentials(credentialsParameterARN, iamCredentials)
	assert.Error(t, err)
}

func TestRetrieveFSxWindowsFileServerDNSName(t *testing.T) {
	fv, _, _, _, fsxClientCreator, _, _, mockFSxClient := setup(t)
	fsxClientOutput := &fsx.DescribeFileSystemsOutput{
		FileSystems: []types.FileSystem{
			{
				FileSystemId: aws.String(fileSystemId),
				DNSName:      aws.String("test"),
			},
		},
	}

	gomock.InOrder(
		fsxClientCreator.EXPECT().NewFSxClient(gomock.Any(), gomock.Any()).Return(mockFSxClient, nil),
		mockFSxClient.EXPECT().DescribeFileSystems(gomock.Any(), gomock.Any()).Return(fsxClientOutput, nil).Times(1),
	)

	iamCredentials := credentials.IAMRoleCredentials{
		CredentialsID: "test-cred-id",
	}

	err := fv.retrieveFileSystemDNSName(fileSystemId, iamCredentials)
	assert.NoError(t, err)

	DNSName := fv.FSxWindowsFileServerDNSName
	assert.Equal(t, "test", DNSName)
}

func TestHandleRootDirectory(t *testing.T) {
	fv1 := &FSxWindowsFileServerResource{}
	fv1.handleRootDirectory("some/path/")

	fv2 := &FSxWindowsFileServerResource{}
	fv2.handleRootDirectory("\\some\\path")

	fv3 := &FSxWindowsFileServerResource{}
	fv3.handleRootDirectory("\\some/path")

	fv4 := &FSxWindowsFileServerResource{}
	fv4.handleRootDirectory("\\")

	assert.Equal(t, "some\\path", fv1.VolumeConfig.RootDirectory)
	assert.Equal(t, "some\\path", fv2.VolumeConfig.RootDirectory)
	assert.Equal(t, "some\\path", fv3.VolumeConfig.RootDirectory)
	assert.Equal(t, "", fv4.VolumeConfig.RootDirectory)
}

func TestGetName(t *testing.T) {
	fv := &FSxWindowsFileServerResource{
		Name: "test",
	}

	assert.Equal(t, "test", fv.GetName())
}

func TestGetVolumeConfig(t *testing.T) {
	fv := &FSxWindowsFileServerResource{
		VolumeConfig: FSxWindowsFileServerVolumeConfig{
			FileSystemID:  "fs-12345678",
			RootDirectory: "root",
			AuthConfig: FSxWindowsFileServerAuthConfig{
				CredentialsParameter: credentialsParameter,
				Domain:               "test",
			},
			HostPath: hostPath,
		},
	}

	volumeConfig := fv.GetVolumeConfig()
	assert.Equal(t, "fs-12345678", volumeConfig.FileSystemID)
	assert.Equal(t, "root", volumeConfig.RootDirectory)
	assert.Equal(t, "arn", volumeConfig.AuthConfig.CredentialsParameter)
	assert.Equal(t, "test", volumeConfig.AuthConfig.Domain)
	assert.Equal(t, `Z:\`, volumeConfig.HostPath)
}

func TestGetCredentials(t *testing.T) {
	fv := &FSxWindowsFileServerResource{
		Credentials: FSxWindowsFileServerCredentials{
			Username: "user",
			Password: "pass",
		},
	}

	credentials := fv.GetCredentials()
	assert.Equal(t, "user", credentials.Username)
	assert.Equal(t, "pass", credentials.Password)
}

func TestGetFileSystemDNSName(t *testing.T) {
	fv := &FSxWindowsFileServerResource{
		FSxWindowsFileServerDNSName: "test",
	}

	assert.Equal(t, "test", fv.GetFileSystemDNSName())
}

func TestSetCredentials(t *testing.T) {
	fv := &FSxWindowsFileServerResource{}
	fv.SetCredentials(FSxWindowsFileServerCredentials{
		Username: "user",
		Password: "pass",
	})

	assert.Equal(t, "user", fv.Credentials.Username)
	assert.Equal(t, "pass", fv.Credentials.Password)
}

func TestSetFileSystemDNSName(t *testing.T) {
	fv := &FSxWindowsFileServerResource{}
	fv.SetFileSystemDNSName("test")
	assert.Equal(t, "test", fv.FSxWindowsFileServerDNSName)
}

func TestSetRootDirectory(t *testing.T) {
	fv := &FSxWindowsFileServerResource{}
	fv.SetRootDirectory("root")
	assert.Equal(t, "root", fv.VolumeConfig.RootDirectory)
}

func fakeExecCommand(command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestHelperProcess", "--", command}
	cs = append(cs, args...)
	cmd := exec.Command(os.Args[0], cs...)
	cmd.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
	return cmd
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	os.Exit(0)
}

func TestPerformHostMount(t *testing.T) {
	fv := &FSxWindowsFileServerResource{}
	execCommand = fakeExecCommand
	defer func() { execCommand = exec.Command }()

	err := fv.performHostMount(`\\amznfsxfp8sdlcw.test.corp.com\share`, `test\user`, `pass`)
	assert.NoError(t, err)
}

func TestRemoveHostMount(t *testing.T) {
	fv, _, _, _, _, _, _, _ := setup(t)
	execCommand = fakeExecCommand

	defer func() {
		execCommand = exec.Command
	}()

	err := fv.removeHostMount("test")
	assert.NoError(t, err)
}

func TestCreateInvalidExecutionRoleCredentialsErr(t *testing.T) {
	fv, credentialsManager, _, _, _, _, _, _ := setup(t)

	DriveLetterAvailable = func(string) bool {
		return true
	}

	defer func() {
		DriveLetterAvailable = utils.IsAvailableDriveLetter
	}()

	creds := credentials.TaskIAMRoleCredentials{}
	gomock.InOrder(
		credentialsManager.EXPECT().GetTaskCredentials(gomock.Any()).Return(creds, false),
	)

	err := fv.Create()
	assert.Error(t, err)
}

func TestCreateUnavailableLocalPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	credentialsManager := mock_credentials.NewMockManager(ctrl)
	ssmClientCreator := mock_ssm_factory.NewMockSSMClientCreator(ctrl)
	asmClientCreator := mock_asm_factory.NewMockClientCreator(ctrl)
	fsxClientCreator := mock_fsx_factory.NewMockFSxClientCreator(ctrl)
	mockSSMClient := mock_ssmiface.NewMockSSMClient(ctrl)
	mockFSxClient := mock_fsxiface.NewMockFSxClient(ctrl)

	fv := &FSxWindowsFileServerResource{
		VolumeConfig: FSxWindowsFileServerVolumeConfig{
			FileSystemID:  fileSystemId,
			RootDirectory: rootDirectory,
			AuthConfig: FSxWindowsFileServerAuthConfig{
				CredentialsParameter: "arn:aws:ssm:us-west-2:123456789012:parameter/test",
				Domain:               domain,
			},
			HostPath: hostPath,
		},
		knownStatusUnsafe:      resourcestatus.ResourceCreated,
		desiredStatusUnsafe:    resourcestatus.ResourceCreated,
		taskARN:                taskARN,
		executionCredentialsID: executionCredentialsID,
	}
	fv.Initialize(
		testConfig,
		&taskresource.ResourceFields{
			ResourceFieldsCommon: &taskresource.ResourceFieldsCommon{
				SSMClientCreator:   ssmClientCreator,
				ASMClientCreator:   asmClientCreator,
				FSxClientCreator:   fsxClientCreator,
				CredentialsManager: credentialsManager,
			},
		}, apitaskstatus.TaskStatusNone, apitaskstatus.TaskRunning)

	ssmTestData := "{\n\"username\": \"user\", \n\"password\": \"pass\"\n}"
	ssmClientOutput := &ssm.GetParametersOutput{
		InvalidParameters: []string{},
		Parameters: []ssmtypes.Parameter{
			ssmtypes.Parameter{
				Name:  aws.String("/test"),
				Value: aws.String(ssmTestData),
			},
		},
	}

	fsxClientOutput := &fsx.DescribeFileSystemsOutput{
		FileSystems: []types.FileSystem{
			{
				FileSystemId: aws.String(fileSystemId),
				DNSName:      aws.String("test"),
			},
		},
	}

	creds := credentials.TaskIAMRoleCredentials{
		ARN: "arn",
		IAMRoleCredentials: credentials.IAMRoleCredentials{
			AccessKeyID:     "id",
			SecretAccessKey: "key",
		},
	}

	gomock.InOrder(
		credentialsManager.EXPECT().GetTaskCredentials(gomock.Any()).Return(creds, true),
		ssmClientCreator.EXPECT().NewSSMClient(gomock.Any(), gomock.Any(), testConfig.InstanceIPCompatibility).Return(mockSSMClient, nil),
		mockSSMClient.EXPECT().GetParameters(gomock.Any(), gomock.Any()).Return(ssmClientOutput, nil).Times(1),
		fsxClientCreator.EXPECT().NewFSxClient(gomock.Any(), gomock.Any()).Return(mockFSxClient, nil),
		mockFSxClient.EXPECT().DescribeFileSystems(gomock.Any(), gomock.Any()).Return(fsxClientOutput, nil).Times(1),
	)

	DriveLetterAvailable = func(string) bool {
		return false
	}
	execCommand = fakeExecCommand

	defer func() {
		DriveLetterAvailable = utils.IsAvailableDriveLetter
		execCommand = exec.Command
	}()

	err := fv.Create()
	assert.NoError(t, err)
}

func TestCreateSSM(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	credentialsManager := mock_credentials.NewMockManager(ctrl)
	ssmClientCreator := mock_ssm_factory.NewMockSSMClientCreator(ctrl)
	asmClientCreator := mock_asm_factory.NewMockClientCreator(ctrl)
	fsxClientCreator := mock_fsx_factory.NewMockFSxClientCreator(ctrl)
	mockSSMClient := mock_ssmiface.NewMockSSMClient(ctrl)
	mockFSxClient := mock_fsxiface.NewMockFSxClient(ctrl)

	fv := &FSxWindowsFileServerResource{
		VolumeConfig: FSxWindowsFileServerVolumeConfig{
			FileSystemID:  fileSystemId,
			RootDirectory: rootDirectory,
			AuthConfig: FSxWindowsFileServerAuthConfig{
				CredentialsParameter: "arn:aws:ssm:us-west-2:123456789012:parameter/test",
				Domain:               domain,
			},
			HostPath: hostPath,
		},
		knownStatusUnsafe:      resourcestatus.ResourceCreated,
		desiredStatusUnsafe:    resourcestatus.ResourceCreated,
		taskARN:                taskARN,
		executionCredentialsID: executionCredentialsID,
	}
	fv.Initialize(
		testConfig,
		&taskresource.ResourceFields{
			ResourceFieldsCommon: &taskresource.ResourceFieldsCommon{
				SSMClientCreator:   ssmClientCreator,
				ASMClientCreator:   asmClientCreator,
				FSxClientCreator:   fsxClientCreator,
				CredentialsManager: credentialsManager,
			},
		}, apitaskstatus.TaskStatusNone, apitaskstatus.TaskRunning)

	ssmTestData := "{\n\"username\": \"user\", \n\"password\": \"pass\"\n}"
	ssmClientOutput := &ssm.GetParametersOutput{
		InvalidParameters: []string{},
		Parameters: []ssmtypes.Parameter{
			ssmtypes.Parameter{
				Name:  aws.String("/test"),
				Value: aws.String(ssmTestData),
			},
		},
	}

	fsxClientOutput := &fsx.DescribeFileSystemsOutput{
		FileSystems: []types.FileSystem{
			{
				FileSystemId: aws.String(fileSystemId),
				DNSName:      aws.String("test"),
			},
		},
	}

	creds := credentials.TaskIAMRoleCredentials{
		ARN: "arn",
		IAMRoleCredentials: credentials.IAMRoleCredentials{
			AccessKeyID:     "id",
			SecretAccessKey: "key",
		},
	}

	gomock.InOrder(
		credentialsManager.EXPECT().GetTaskCredentials(gomock.Any()).Return(creds, true),
		ssmClientCreator.EXPECT().NewSSMClient(gomock.Any(), gomock.Any(), testConfig.InstanceIPCompatibility).Return(mockSSMClient, nil),
		mockSSMClient.EXPECT().GetParameters(gomock.Any(), gomock.Any()).Return(ssmClientOutput, nil).Times(1),
		fsxClientCreator.EXPECT().NewFSxClient(gomock.Any(), gomock.Any()).Return(mockFSxClient, nil),
		mockFSxClient.EXPECT().DescribeFileSystems(gomock.Any(), gomock.Any()).Return(fsxClientOutput, nil).Times(1),
	)

	DriveLetterAvailable = func(string) bool {
		return true
	}
	execCommand = fakeExecCommand

	defer func() {
		DriveLetterAvailable = utils.IsAvailableDriveLetter
		execCommand = exec.Command
	}()

	err := fv.Create()
	assert.NoError(t, err)
}

func TestCreateASM(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	credentialsManager := mock_credentials.NewMockManager(ctrl)
	ssmClientCreator := mock_ssm_factory.NewMockSSMClientCreator(ctrl)
	asmClientCreator := mock_asm_factory.NewMockClientCreator(ctrl)
	fsxClientCreator := mock_fsx_factory.NewMockFSxClientCreator(ctrl)
	mockASMClient := mock_secretsmanageriface.NewMockSecretsManagerAPI(ctrl)
	mockFSxClient := mock_fsxiface.NewMockFSxClient(ctrl)

	credentialsParameter := "arn:aws:secretsmanager:us-east-1:123456789012:secret:testing/some-random-name"

	fv := &FSxWindowsFileServerResource{
		VolumeConfig: FSxWindowsFileServerVolumeConfig{
			FileSystemID:  fileSystemId,
			RootDirectory: rootDirectory,
			AuthConfig: FSxWindowsFileServerAuthConfig{
				CredentialsParameter: "arn:aws:secretsmanager:us-east-1:123456789012:secret:testing/some-random-name",
				Domain:               domain,
			},
			HostPath: hostPath,
		},
		knownStatusUnsafe:      resourcestatus.ResourceCreated,
		desiredStatusUnsafe:    resourcestatus.ResourceCreated,
		taskARN:                taskARN,
		executionCredentialsID: executionCredentialsID,
	}
	fv.Initialize(
		&config.Config{},
		&taskresource.ResourceFields{
			ResourceFieldsCommon: &taskresource.ResourceFieldsCommon{
				SSMClientCreator:   ssmClientCreator,
				ASMClientCreator:   asmClientCreator,
				FSxClientCreator:   fsxClientCreator,
				CredentialsManager: credentialsManager,
			},
		}, apitaskstatus.TaskStatusNone, apitaskstatus.TaskRunning)

	asmTestData := "{\"username\":\"user\",\"password\":\"pass\"}"
	asmClientOutput := &secretsmanager.GetSecretValueOutput{
		SecretString: aws.String(asmTestData),
	}

	fsxClientOutput := &fsx.DescribeFileSystemsOutput{
		FileSystems: []types.FileSystem{
			{
				FileSystemId: aws.String(fileSystemId),
				DNSName:      aws.String("test"),
			},
		},
	}

	creds := credentials.TaskIAMRoleCredentials{
		ARN: "arn",
		IAMRoleCredentials: credentials.IAMRoleCredentials{
			AccessKeyID:     "id",
			SecretAccessKey: "key",
		},
	}

	gomock.InOrder(
		credentialsManager.EXPECT().GetTaskCredentials(gomock.Any()).Return(creds, true),
		asmClientCreator.EXPECT().NewASMClient(gomock.Any(), gomock.Any()).Return(mockASMClient, nil),
		mockASMClient.EXPECT().GetSecretValue(
			gomock.Any(),
			gomock.Any(),
			gomock.Any(),
		).Do(func(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) {
			assert.Equal(t, aws.ToString(in.SecretId), credentialsParameter)
		}).Return(asmClientOutput, nil),
		fsxClientCreator.EXPECT().NewFSxClient(gomock.Any(), gomock.Any()).Return(mockFSxClient, nil),
		mockFSxClient.EXPECT().DescribeFileSystems(gomock.Any(), gomock.Any()).Return(fsxClientOutput, nil).Times(1),
	)

	DriveLetterAvailable = func(string) bool {
		return true
	}
	execCommand = fakeExecCommand

	defer func() {
		DriveLetterAvailable = utils.IsAvailableDriveLetter
		execCommand = exec.Command
	}()

	err := fv.Create()
	assert.NoError(t, err)
}

func TestClearFSxWindowsFileServerResource(t *testing.T) {
	fv := &FSxWindowsFileServerResource{VolumeConfig: FSxWindowsFileServerVolumeConfig{HostPath: hostPath}}

	execCommand = fakeExecCommand
	defer func() { execCommand = exec.Command }()

	err := fv.Cleanup()
	assert.NoError(t, err)
}

func TestSpecialCharactersInPasswordPSCommand(t *testing.T) {
	username := "Administrator"
	password := "AWS@`~!@#$var%^&*()/1asd"

	credsCommand := fmt.Sprintf(psCredentialCommandFormat, username, password)

	// Perform actual exec to determine if the credentials are generated.
	// Go tests are platform specific and therefore, this would work.
	cmd := exec.Command("powershell.exe", credsCommand)
	_, err := cmd.CombinedOutput()

	assert.NoError(t, err)
}
