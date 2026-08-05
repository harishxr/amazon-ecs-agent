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

package mpsdaemon

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	resourcestatus "github.com/aws/amazon-ecs-agent/agent/taskresource/status"
	mock_execwrapper "github.com/aws/amazon-ecs-agent/ecs-agent/utils/execwrapper/mocks"
	"github.com/aws/amazon-ecs-agent/ecs-agent/utils/mps"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
)

// shrinkRetries collapses the retry delays so tests don't sleep for real, and
// restores them (and statFunc) on cleanup.
func shrinkRetries(t *testing.T) {
	t.Helper()
	origMin, origMax, origAttempts := probeMinRetryDelay, probeMaxRetryDelay, probeMaxAttempts
	origStat := statFunc
	probeMinRetryDelay = time.Millisecond
	probeMaxRetryDelay = 2 * time.Millisecond
	t.Cleanup(func() {
		probeMinRetryDelay, probeMaxRetryDelay, probeMaxAttempts = origMin, origMax, origAttempts
		statFunc = origStat
	})
}

// expectProbe wires one full ProbeControlDaemon call (context + command + stdin
// + CombinedOutput) returning the given output/err.
func expectProbe(mockExec *mock_execwrapper.MockExec, mockCmd *mock_execwrapper.MockCmd, out []byte, err error) {
	mockExec.EXPECT().NewExecContextWithTimeout(gomock.Any(), mps.ProbeTimeout).
		DoAndReturn(func(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
			return context.WithTimeout(parent, d)
		})
	mockExec.EXPECT().CommandContext(gomock.Any(), mps.ControlBinary).Return(mockCmd)
	mockCmd.EXPECT().SetIOStreams(gomock.Any(), gomock.Any(), gomock.Any())
	mockCmd.EXPECT().CombinedOutput().Return(out, err)
}

func TestCreateServingDaemonPasses(t *testing.T) {
	shrinkRetries(t)
	statFunc = func(string) (os.FileInfo, error) { return nil, nil } // pipe dir present

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := mock_execwrapper.NewMockExec(ctrl)
	mockCmd := mock_execwrapper.NewMockCmd(ctrl)
	expectProbe(mockExec, mockCmd, []byte("100.0\n"), nil)

	r := NewMPSDaemonResource("arn", mockExec, mps.ProbeCommand)
	assert.NoError(t, r.Create(), "a serving daemon must let the gate pass")
	assert.Empty(t, r.GetTerminalReason(), "a passing gate records no terminal reason")
}

func TestCreateMissingPipeDirBlocks(t *testing.T) {
	shrinkRetries(t)
	probeMaxAttempts = 2
	statFunc = func(string) (os.FileInfo, error) { return nil, errors.New("no such file or directory") }

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	// No probe should run: a missing pipe dir short-circuits before exec.
	mockExec := mock_execwrapper.NewMockExec(ctrl)

	r := NewMPSDaemonResource("arn", mockExec, mps.ProbeCommand)
	err := r.Create()
	assert.Error(t, err, "a missing pipe directory must block the task")
	assert.Contains(t, err.Error(), "pipe directory")
	assert.Contains(t, r.GetTerminalReason(), "pipe directory")
}

func TestCreateDeadDaemonBlocksAfterRetries(t *testing.T) {
	shrinkRetries(t)
	probeMaxAttempts = 2
	statFunc = func(string) (os.FileInfo, error) { return nil, nil } // pipe dir present

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := mock_execwrapper.NewMockExec(ctrl)
	mockCmd := mock_execwrapper.NewMockCmd(ctrl)
	exitErr := &os.PathError{} // any error type; ConvertToExitError decides
	// Both attempts fail with a nonzero exit.
	for i := 0; i < 2; i++ {
		expectProbe(mockExec, mockCmd, []byte("connection failed"), exitErr)
		mockExec.EXPECT().ConvertToExitError(exitErr).Return(nil, false)
	}

	r := NewMPSDaemonResource("arn", mockExec, mps.ProbeCommand)
	err := r.Create()
	assert.Error(t, err, "a dead daemon must block the task after the retry budget")
	assert.Contains(t, err.Error(), "not serving")
	assert.Contains(t, r.GetTerminalReason(), "not serving")
}

func TestCreateRecoversOnLaterAttempt(t *testing.T) {
	shrinkRetries(t)
	probeMaxAttempts = 3
	statFunc = func(string) (os.FileInfo, error) { return nil, nil }

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := mock_execwrapper.NewMockExec(ctrl)
	mockCmd := mock_execwrapper.NewMockCmd(ctrl)
	// First attempt fails, second succeeds (daemon came back mid-restart).
	badErr := &os.PathError{}
	expectProbe(mockExec, mockCmd, []byte("fail"), badErr)
	mockExec.EXPECT().ConvertToExitError(badErr).Return(nil, false)
	expectProbe(mockExec, mockCmd, []byte("100.0"), nil)

	r := NewMPSDaemonResource("arn", mockExec, mps.ProbeCommand)
	assert.NoError(t, r.Create(), "a daemon that recovers within the budget must pass")
	assert.Empty(t, r.GetTerminalReason())
}

func TestSteadyStateAndTransitions(t *testing.T) {
	r := NewMPSDaemonResource("arn", nil, "")
	assert.Equal(t, resourcestatus.ResourceStatus(MPSDaemonVerified), r.SteadyState())
	assert.Equal(t, resourcestatus.ResourceStatus(MPSDaemonRemoved), r.TerminalStatus())
	assert.Equal(t, "VERIFIED", r.StatusString(resourcestatus.ResourceStatus(MPSDaemonVerified)))
	// Unknown transition target is rejected.
	assert.Error(t, r.ApplyTransition(resourcestatus.ResourceStatus(MPSDaemonRemoved)))
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	r := NewMPSDaemonResource("task-arn", nil, mps.ProbeCommand)
	r.SetDesiredStatus(resourcestatus.ResourceStatus(MPSDaemonVerified))
	r.SetKnownStatus(resourcestatus.ResourceStatus(MPSDaemonStatusNone))
	b, err := r.MarshalJSON()
	assert.NoError(t, err)

	restored := &MPSDaemonResource{}
	assert.NoError(t, restored.UnmarshalJSON(b))
	assert.Equal(t, "task-arn", restored.taskARN)
	assert.Equal(t, mps.ProbeCommand, restored.probeCommand)
	assert.Equal(t, resourcestatus.ResourceStatus(MPSDaemonVerified), restored.GetDesiredStatus())
}
