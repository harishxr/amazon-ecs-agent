//go:build unit
// +build unit

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

package ecs

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/aws/amazon-ecs-agent/ecs-agent/api/attachment"
	apicontainerstatus "github.com/aws/amazon-ecs-agent/ecs-agent/api/container/status"
	mock_statechange "github.com/aws/amazon-ecs-agent/ecs-agent/api/ecs/mocks/statechange"
	apitaskstatus "github.com/aws/amazon-ecs-agent/ecs-agent/api/task/status"
	ni "github.com/aws/amazon-ecs-agent/ecs-agent/netlib/model/networkinterface"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
)

const (
	containerName = "container"
	taskArn       = "task_arn"
	attachmentArn = "eni_arn"
)

var dummyTime = time.Time{}

func TestContainerStateChangeString(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	metadataGetter := mock_statechange.NewMockContainerMetadataGetter(ctrl)
	metadataGetter.EXPECT().GetContainerIsNil().Return(false).AnyTimes()
	metadataGetter.EXPECT().GetContainerSentStatusString().Return(apicontainerstatus.ContainerRunning.String()).
		AnyTimes()
	metadataGetter.EXPECT().GetContainerRuntimeID().Return("runtimeid").AnyTimes()
	metadataGetter.EXPECT().GetContainerIsEssential().Return(true).AnyTimes()

	change := &ContainerStateChange{
		ContainerName: containerName,
		Status:        apicontainerstatus.ContainerRunning,
		ExitCode:      aws.Int(1),
		Reason:        "reason",
		NetworkBindings: []types.NetworkBinding{
			{
				ContainerPort: aws.Int32(1),
				HostPort:      aws.Int32(2),
				BindIP:        aws.String("1.2.3.4"),
				Protocol:      "udp",
			},
		},
		MetadataGetter: metadataGetter,
	}

	expectedStr := fmt.Sprintf("containerName=%s"+
		" containerStatus=%s"+
		" containerExitCode=%s"+
		" containerReason=%s"+
		" containerNetworkBindings=%v"+
		" containerKnownSentStatus=%s"+
		" containerRuntimeID=%s"+
		" containerIsEssential=%v",
		change.ContainerName,
		change.Status.String(),
		strconv.Itoa(*change.ExitCode),
		change.Reason,
		change.NetworkBindings,
		change.MetadataGetter.GetContainerSentStatusString(),
		change.MetadataGetter.GetContainerRuntimeID(),
		change.MetadataGetter.GetContainerIsEssential(),
	)

	assert.Equal(t, expectedStr, change.String())
}

func TestTaskStateChangeString(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	metadataGetter := mock_statechange.NewMockTaskMetadataGetter(ctrl)
	metadataGetter.EXPECT().GetTaskIsNil().Return(false).AnyTimes()
	metadataGetter.EXPECT().GetTaskSentStatusString().Return(apitaskstatus.TaskRunning.String()).
		AnyTimes()
	metadataGetter.EXPECT().GetTaskPullStartedAt().Return(dummyTime).AnyTimes()
	metadataGetter.EXPECT().GetTaskPullStoppedAt().Return(dummyTime).AnyTimes()
	metadataGetter.EXPECT().GetTaskExecutionStoppedAt().Return(dummyTime).AnyTimes()

	change := &TaskStateChange{
		TaskARN: taskArn,
		Status:  apitaskstatus.TaskRunning,
		Attachment: &ni.ENIAttachment{
			AttachmentInfo: attachment.AttachmentInfo{
				AttachmentARN: attachmentArn,
			},
		},
		Containers: []types.ContainerStateChange{
			{
				ContainerName: aws.String(containerName),
			},
		},
		ManagedAgents: []types.ManagedAgentStateChange{
			{
				ManagedAgentName: types.ManagedAgentNameExecuteCommandAgent,
			},
		},
		MetadataGetter: metadataGetter,
	}

	assert.Len(t, change.Containers, 1)
	assert.Len(t, change.ManagedAgents, 1)

	expectedStr := fmt.Sprintf("%s -> %s"+
		", Known Sent: %s"+
		", PullStartedAt: %s"+
		", PullStoppedAt: %s"+
		", ExecutionStoppedAt: %s"+
		", "+change.Attachment.String()+
		", container change: "+prettify(change.Containers[0])+
		", managed agent: "+prettify(change.ManagedAgents[0]),
		change.TaskARN,
		change.Status.String(),
		change.MetadataGetter.GetTaskSentStatusString(),
		change.MetadataGetter.GetTaskPullStartedAt(),
		change.MetadataGetter.GetTaskPullStoppedAt(),
		change.MetadataGetter.GetTaskExecutionStoppedAt(),
	)

	assert.Equal(t, expectedStr, change.String())
}

func TestAttachmentStateChangeString(t *testing.T) {
	change := &AttachmentStateChange{
		Attachment: &ni.ENIAttachment{
			AttachmentInfo: attachment.AttachmentInfo{
				AttachmentARN:    attachmentArn,
				Status:           attachment.AttachmentAttached,
				TaskARN:          taskArn,
				AttachStatusSent: true,
				ExpiresAt:        dummyTime,
			},
		},
	}

	expectedStr := fmt.Sprintf("%s -> %v, %s", change.Attachment.GetAttachmentARN(),
		change.Attachment.GetAttachmentStatus(), change.Attachment.String())

	assert.Equal(t, expectedStr, change.String())
}
