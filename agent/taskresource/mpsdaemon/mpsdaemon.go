//go:build linux
// +build linux

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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	apicontainer "github.com/aws/amazon-ecs-agent/agent/api/container"
	"github.com/aws/amazon-ecs-agent/agent/config"
	"github.com/aws/amazon-ecs-agent/agent/taskresource"
	resourcestatus "github.com/aws/amazon-ecs-agent/agent/taskresource/status"
	apicontainerstatus "github.com/aws/amazon-ecs-agent/ecs-agent/api/container/status"
	"github.com/aws/amazon-ecs-agent/ecs-agent/api/task/status"
	"github.com/aws/amazon-ecs-agent/ecs-agent/logger"
	"github.com/aws/amazon-ecs-agent/ecs-agent/logger/field"
	"github.com/aws/amazon-ecs-agent/ecs-agent/utils/execwrapper"
	"github.com/aws/amazon-ecs-agent/ecs-agent/utils/mps"
	"github.com/aws/amazon-ecs-agent/ecs-agent/utils/retry"
)

// ResourceName is the key used in the task resources map.
const ResourceName = "mps-daemon-health"

// Retry parameters for the health check. systemd restarts the daemon with
// RestartSec=1, so the check is retried briefly to ride over a restart rather
// than failing a task that would recover in ~1s. These are vars, not consts, so
// tests can shrink the delays.
var (
	probeMaxAttempts     = 3
	probeMinRetryDelay   = 2 * time.Second
	probeMaxRetryDelay   = 4 * time.Second
	probeRetryJitter     = 0.2
	probeRetryMultiplier = 2.0
)

// statFunc checks for the MPS control pipe directory; a var so tests can stub it.
var statFunc = os.Stat

// MPSDaemonResource verifies the NVIDIA MPS control daemon is functionally
// serving before an MPS task's containers are allowed to be created.
type MPSDaemonResource struct {
	taskARN string
	// probeCommand is the control command sent to the front-end. Defaults to
	// mps.ProbeCommand.
	probeCommand string
	exec         execwrapper.Exec

	createdAt           time.Time
	desiredStatusUnsafe resourcestatus.ResourceStatus
	knownStatusUnsafe   resourcestatus.ResourceStatus
	appliedStatus       resourcestatus.ResourceStatus
	statusToTransitions map[resourcestatus.ResourceStatus]func() error
	terminalReason      string
	terminalReasonOnce  sync.Once
	lock                sync.RWMutex
}

// NewMPSDaemonResource returns a new health-gate resource for a task.
func NewMPSDaemonResource(taskARN string, exec execwrapper.Exec, probeCommand string) *MPSDaemonResource {
	if probeCommand == "" {
		probeCommand = mps.ProbeCommand
	}
	r := &MPSDaemonResource{
		taskARN:      taskARN,
		exec:         exec,
		probeCommand: probeCommand,
	}
	r.initStatusToTransitionFunction()
	return r
}

func (r *MPSDaemonResource) initStatusToTransitionFunction() {
	r.statusToTransitions = map[resourcestatus.ResourceStatus]func() error{
		resourcestatus.ResourceStatus(MPSDaemonVerified): r.Create,
	}
}

// Create runs the health check with a bounded retry. It returns nil if any
// attempt finds the daemon serving; if all attempts fail, it records the last
// error as the terminal reason and returns it, which STOPs the task.
func (r *MPSDaemonResource) Create() error {
	if r.exec == nil {
		r.exec = execwrapper.NewExec()
	}

	backoff := retry.NewExponentialBackoff(probeMinRetryDelay, probeMaxRetryDelay,
		probeRetryJitter, probeRetryMultiplier)

	attempt := 0
	err := retry.RetryNWithBackoffCtx(context.Background(), backoff, probeMaxAttempts,
		func() error {
			attempt++
			return r.checkOnce(attempt)
		})
	if err != nil {
		reason := err.Error()
		r.setTerminalReason(reason)
		logger.Error("MPS daemon health gate: blocking task (retry budget exhausted)", logger.Fields{
			field.TaskARN: r.taskARN,
			"attempts":    attempt,
			field.Reason:  reason,
		})
		return errors.New(reason)
	}
	return nil
}

// checkOnce runs one attempt: pipe-dir presence, then the probe. It returns an
// error carrying the failure evidence when the daemon is not serving. It does
// not set the terminal reason (Create does, after the retry is exhausted).
func (r *MPSDaemonResource) checkOnce(attempt int) error {
	// A missing pipe directory means the daemon isn't up; skip the probe.
	if _, err := statFunc(mps.PipeDirectory); err != nil {
		reason := fmt.Sprintf("MPS control pipe directory %s missing (daemon not started): %v",
			mps.PipeDirectory, err)
		logger.Warn("MPS daemon health gate: attempt failed", logger.Fields{
			field.TaskARN: r.taskARN,
			"attempt":     attempt,
			field.Reason:  reason,
		})
		return errors.New(reason)
	}

	res := mps.ProbeControlDaemon(r.exec, r.probeCommand)
	logger.Info("MPS daemon health gate: probe complete", logger.Fields{
		field.TaskARN: r.taskARN,
		"attempt":     attempt,
		"command":     r.probeCommand,
		"exitCode":    res.ExitCode,
		"stdout":      res.Stdout,
		"latencyMs":   res.Latency.Milliseconds(),
		"timedOut":    res.TimedOut,
	})
	if res.Err != nil {
		reason := fmt.Sprintf("MPS control daemon not serving (probe %q exit=%d timedOut=%t): %v",
			r.probeCommand, res.ExitCode, res.TimedOut, res.Err)
		logger.Warn("MPS daemon health gate: attempt failed", logger.Fields{
			field.TaskARN: r.taskARN,
			"attempt":     attempt,
			field.Reason:  reason,
		})
		return errors.New(reason)
	}
	return nil
}

// Cleanup is a no-op: the health gate owns no host state.
func (r *MPSDaemonResource) Cleanup() error {
	return nil
}

func (r *MPSDaemonResource) setTerminalReason(reason string) {
	r.terminalReasonOnce.Do(func() {
		r.lock.Lock()
		defer r.lock.Unlock()
		r.terminalReason = reason
	})
}

// GetTerminalReason returns why the resource failed to provision.
func (r *MPSDaemonResource) GetTerminalReason() string {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.terminalReason
}

// GetName returns the unique name of the resource.
func (r *MPSDaemonResource) GetName() string { return ResourceName }

// SetDesiredStatus sets the desired status of the resource.
func (r *MPSDaemonResource) SetDesiredStatus(status resourcestatus.ResourceStatus) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.desiredStatusUnsafe = status
}

// GetDesiredStatus gets the desired status of the resource.
func (r *MPSDaemonResource) GetDesiredStatus() resourcestatus.ResourceStatus {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.desiredStatusUnsafe
}

// SetKnownStatus sets the known status of the resource.
func (r *MPSDaemonResource) SetKnownStatus(status resourcestatus.ResourceStatus) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.knownStatusUnsafe = status
	r.updateAppliedStatusUnsafe(status)
}

func (r *MPSDaemonResource) updateAppliedStatusUnsafe(knownStatus resourcestatus.ResourceStatus) {
	if r.appliedStatus == resourcestatus.ResourceStatus(MPSDaemonStatusNone) {
		return
	}
	if r.appliedStatus <= knownStatus {
		r.appliedStatus = resourcestatus.ResourceStatus(MPSDaemonStatusNone)
	}
}

// GetKnownStatus gets the known status of the resource.
func (r *MPSDaemonResource) GetKnownStatus() resourcestatus.ResourceStatus {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.knownStatusUnsafe
}

// SetCreatedAt sets the timestamp for the resource's creation time.
func (r *MPSDaemonResource) SetCreatedAt(createdAt time.Time) {
	if createdAt.IsZero() {
		return
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	r.createdAt = createdAt
}

// GetCreatedAt gets the timestamp for the resource's creation time.
func (r *MPSDaemonResource) GetCreatedAt() time.Time {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.createdAt
}

// DesiredTerminal returns true if the resource's desired state is terminal.
func (r *MPSDaemonResource) DesiredTerminal() bool {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.desiredStatusUnsafe == resourcestatus.ResourceStatus(MPSDaemonRemoved)
}

// KnownCreated returns true when the daemon has been verified.
func (r *MPSDaemonResource) KnownCreated() bool {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.knownStatusUnsafe == resourcestatus.ResourceStatus(MPSDaemonVerified)
}

// TerminalStatus returns the last transition state of the resource.
func (r *MPSDaemonResource) TerminalStatus() resourcestatus.ResourceStatus {
	return resourcestatus.ResourceStatus(MPSDaemonRemoved)
}

// NextKnownState returns the resource's next state.
func (r *MPSDaemonResource) NextKnownState() resourcestatus.ResourceStatus {
	return r.GetKnownStatus() + 1
}

// ApplyTransition calls the function required to move to the specified status.
func (r *MPSDaemonResource) ApplyTransition(nextState resourcestatus.ResourceStatus) error {
	transitionFunc, ok := r.statusToTransitions[nextState]
	if !ok {
		return fmt.Errorf("resource [%s]: transition to %s impossible", r.GetName(),
			r.StatusString(nextState))
	}
	return transitionFunc()
}

// SteadyState returns the transition state of the resource defined as "ready".
func (r *MPSDaemonResource) SteadyState() resourcestatus.ResourceStatus {
	return resourcestatus.ResourceStatus(MPSDaemonVerified)
}

// SetAppliedStatus sets the applied status of the resource and returns whether
// the resource is already in a transition.
func (r *MPSDaemonResource) SetAppliedStatus(status resourcestatus.ResourceStatus) bool {
	r.lock.Lock()
	defer r.lock.Unlock()
	if r.appliedStatus != resourcestatus.ResourceStatus(MPSDaemonStatusNone) {
		return false
	}
	r.appliedStatus = status
	return true
}

// GetAppliedStatus gets the applied status of the resource.
func (r *MPSDaemonResource) GetAppliedStatus() resourcestatus.ResourceStatus {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return r.appliedStatus
}

// StatusString returns the string form of a resource status.
func (r *MPSDaemonResource) StatusString(status resourcestatus.ResourceStatus) string {
	return MPSDaemonStatus(status).String()
}

// DependOnTaskNetwork reports whether the resource needs task network setup.
func (r *MPSDaemonResource) DependOnTaskNetwork() bool { return false }

// RequiresExecutionRoleCredentials reports whether the resource needs execution
// role credentials.
func (r *MPSDaemonResource) RequiresExecutionRoleCredentials() bool { return false }

func (r *MPSDaemonResource) BuildContainerDependency(containerName string,
	satisfied apicontainerstatus.ContainerStatus, dependent resourcestatus.ResourceStatus) {
}

// GetContainerDependencies returns the resource's dependent containers; this
// gate has none of its own.
func (r *MPSDaemonResource) GetContainerDependencies(dependent resourcestatus.ResourceStatus) []apicontainer.ContainerDependency {
	return nil
}

// Initialize re-wires the exec dependency (from ResourceFields) after unmarshal.
func (r *MPSDaemonResource) Initialize(
	cfg *config.Config,
	resourceFields *taskresource.ResourceFields,
	taskKnownStatus status.TaskStatus,
	taskDesiredStatus status.TaskStatus) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.initStatusToTransitionFunction()
	if r.probeCommand == "" {
		r.probeCommand = mps.ProbeCommand
	}
	if resourceFields != nil && resourceFields.Exec != nil {
		r.exec = resourceFields.Exec
	}
	if r.exec == nil {
		r.exec = execwrapper.NewExec()
	}
}

// mpsDaemonResourceJSON is the marshalling shadow struct.
type mpsDaemonResourceJSON struct {
	TaskARN       string           `json:"taskARN"`
	ProbeCommand  string           `json:"probeCommand"`
	CreatedAt     time.Time        `json:"createdAt,omitempty"`
	DesiredStatus *MPSDaemonStatus `json:"desiredStatus"`
	KnownStatus   *MPSDaemonStatus `json:"knownStatus"`
}

// MarshalJSON serializes the resource.
func (r *MPSDaemonResource) MarshalJSON() ([]byte, error) {
	if r == nil {
		return nil, errors.New("mpsdaemon resource is nil")
	}
	desired := MPSDaemonStatus(r.GetDesiredStatus())
	known := MPSDaemonStatus(r.GetKnownStatus())
	return json.Marshal(mpsDaemonResourceJSON{
		TaskARN:       r.taskARN,
		ProbeCommand:  r.probeCommand,
		CreatedAt:     r.GetCreatedAt(),
		DesiredStatus: &desired,
		KnownStatus:   &known,
	})
}

// UnmarshalJSON deserializes the resource.
func (r *MPSDaemonResource) UnmarshalJSON(b []byte) error {
	temp := mpsDaemonResourceJSON{}
	if err := json.Unmarshal(b, &temp); err != nil {
		return err
	}
	r.taskARN = temp.TaskARN
	r.probeCommand = temp.ProbeCommand
	if temp.DesiredStatus != nil {
		r.SetDesiredStatus(resourcestatus.ResourceStatus(*temp.DesiredStatus))
	}
	if temp.KnownStatus != nil {
		r.SetKnownStatus(resourcestatus.ResourceStatus(*temp.KnownStatus))
	}
	return nil
}
