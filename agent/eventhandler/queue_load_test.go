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

package eventhandler

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aws/amazon-ecs-agent/agent/api"
	apicontainer "github.com/aws/amazon-ecs-agent/agent/api/container"
	apitask "github.com/aws/amazon-ecs-agent/agent/api/task"
	"github.com/aws/amazon-ecs-agent/agent/data"
	"github.com/aws/amazon-ecs-agent/agent/dockerstate"
	"github.com/aws/amazon-ecs-agent/ecs-agent/api/ecs"
	mock_ecs "github.com/aws/amazon-ecs-agent/ecs-agent/api/ecs/mocks"
	apicontainerstatus "github.com/aws/amazon-ecs-agent/ecs-agent/api/container/status"
	apitaskstatus "github.com/aws/amazon-ecs-agent/ecs-agent/api/task/status"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
)

// TestQueueHighLoad tests the queue behavior under high load with many concurrent events
func TestQueueHighLoad(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	client := mock_ecs.NewMockECSClient(ctrl)

	ctx, cancel := context.WithCancel(context.Background())
	handler := NewTaskHandler(ctx, data.NewNoopClient(), dockerstate.NewTaskEngineState(), client)
	defer cancel()

	// Number of tasks to create
	numTasks := 50
	
	// Create a wait group to wait for all events to be processed
	var wg sync.WaitGroup
	wg.Add(numTasks)
	
	// Set up expectations for the API calls
	client.EXPECT().SubmitTaskStateChange(gomock.Any()).Do(func(change ecs.TaskStateChange) {
		// Verify that the task has the correct number of containers
		assert.Equal(t, 2, len(change.Containers))
		wg.Done()
	}).Times(numTasks)
	
	// Create and add events for multiple tasks
	for i := 0; i < numTasks; i++ {
		taskARN := fmt.Sprintf("taskARN-%d", i)
		
		// Create container events for each task
		contEvent1 := api.ContainerStateChange{
			TaskArn: taskARN, 
			ContainerName: fmt.Sprintf("container1-%d", i), 
			Status: apicontainerstatus.ContainerRunning, 
			Container: &apicontainer.Container{},
		}
		
		contEvent2 := api.ContainerStateChange{
			TaskArn: taskARN, 
			ContainerName: fmt.Sprintf("container2-%d", i), 
			Status: apicontainerstatus.ContainerRunning, 
			Container: &apicontainer.Container{},
		}
		
		// Create task event
		taskEvent := api.TaskStateChange{
			TaskARN: taskARN, 
			Status: apitaskstatus.TaskRunning, 
			Task: &apitask.Task{},
		}
		
		// Add events to the handler
		handler.AddStateChangeEvent(contEvent1, client)
		handler.AddStateChangeEvent(contEvent2, client)
		handler.AddStateChangeEvent(taskEvent, client)
	}
	
	// Wait for all events to be processed with a timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	
	select {
	case <-done:
		// All events processed successfully
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for events to be processed")
	}
	
	// Verify that all container events have been processed
	assert.Equal(t, 0, len(handler.tasksToContainerStates))
	
	// Wait for task events to be removed from the tasksToEvents map
	timeout := time.After(5 * time.Second)
	for {
		if getTasksToEventsLen(handler) == 0 {
			break
		}
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for tasksToEvents map to be empty")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

// TestQueueConcurrentAccess tests that the queue can handle concurrent access from multiple goroutines
func TestQueueConcurrentAccess(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	client := mock_ecs.NewMockECSClient(ctrl)

	ctx, cancel := context.WithCancel(context.Background())
	handler := NewTaskHandler(ctx, data.NewNoopClient(), dockerstate.NewTaskEngineState(), client)
	defer cancel()

	// Number of goroutines to create
	numGoroutines := 10
	// Number of events per goroutine
	eventsPerGoroutine := 10
	
	// Total number of task events that will be submitted
	totalEvents := numGoroutines * eventsPerGoroutine
	
	// Create a wait group to wait for all events to be processed
	var wg sync.WaitGroup
	wg.Add(totalEvents)
	
	// Set up expectations for the API calls
	client.EXPECT().SubmitTaskStateChange(gomock.Any()).Do(func(change ecs.TaskStateChange) {
		wg.Done()
	}).Times(totalEvents)
	
	// Create a wait group to synchronize goroutine start
	var startWg sync.WaitGroup
	startWg.Add(numGoroutines)
	
	// Start multiple goroutines to add events concurrently
	for i := 0; i < numGoroutines; i++ {
		go func(routineID int) {
			// Signal that this goroutine is ready
			startWg.Done()
			// Wait for all goroutines to be ready
			startWg.Wait()
			
			// Add events for this goroutine
			for j := 0; j < eventsPerGoroutine; j++ {
				taskARN := fmt.Sprintf("taskARN-%d-%d", routineID, j)
				
				// Create task event
				taskEvent := api.TaskStateChange{
					TaskARN: taskARN, 
					Status: apitaskstatus.TaskRunning, 
					Task: &apitask.Task{},
				}
				
				// Add event to the handler
				handler.AddStateChangeEvent(taskEvent, client)
			}
		}(i)
	}
	
	// Wait for all events to be processed with a timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	
	select {
	case <-done:
		// All events processed successfully
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for events to be processed")
	}
	
	// Wait for task events to be removed from the tasksToEvents map
	timeout := time.After(5 * time.Second)
	for {
		if getTasksToEventsLen(handler) == 0 {
			break
		}
		select {
		case <-timeout:
			t.Fatal("Timeout waiting for tasksToEvents map to be empty")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}