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

// MOCK -- LIVE TEST SCAFFOLDING ONLY. NOT FOR THE CR. DELETE BEFORE MERGE.
//
// The daemon health gate fires only for MPS tasks (task.IsMPS()), which requires
// container.MPSConfig to be set by applyGPUResourceRequirements from the ACS
// sharingStrategy. The real RegisterTaskDefinition -> ACS path does not carry
// sharingStrategy yet, so a plain run-task leaves MPSConfig nil and the gate
// never fires. This synthesizes the real ecsacs GPU sharingStrategy from a
// docker label that survives RTD -> ACS, so the unchanged real consume path and
// health gate run on it:
//
//	mps.mock.memory-mib   (required, MiB, > 0)  -> NvidiaMps.Memory
//
// Delete this file (and its call in TaskFromACS) once ACS carries sharingStrategy.

package task

import (
	"encoding/json"
	"strconv"

	"github.com/aws/amazon-ecs-agent/ecs-agent/acs/model/ecsacs"
	"github.com/aws/amazon-ecs-agent/ecs-agent/logger"
	"github.com/aws/amazon-ecs-agent/ecs-agent/logger/field"
	"github.com/aws/aws-sdk-go-v2/aws"
)

const mockMPSMemoryLabel = "mps.mock.memory-mib"

type dockerConfigLabels struct {
	Labels map[string]string `json:"Labels"`
}

// mockInjectMPSSharingStrategyFromLabels appends a real ecsacs GPU
// sharingStrategy to each wire container carrying the mps.mock.memory-mib label.
// Runs in TaskFromACS before applyGPUResourceRequirements so the real consume
// path maps it onto MPSConfig (which makes IsMPS() true and fires the gate).
func mockInjectMPSSharingStrategyFromLabels(acsTask *ecsacs.Task) {
	for _, wireContainer := range acsTask.Containers {
		if wireContainer == nil || wireContainer.DockerConfig == nil || wireContainer.DockerConfig.Config == nil {
			continue
		}
		var cfg dockerConfigLabels
		if err := json.Unmarshal([]byte(aws.ToString(wireContainer.DockerConfig.Config)), &cfg); err != nil {
			continue
		}
		memStr, ok := cfg.Labels[mockMPSMemoryLabel]
		if !ok {
			continue
		}
		mem, err := strconv.ParseInt(memStr, 10, 64)
		if err != nil || mem <= 0 {
			continue
		}
		wireContainer.ResourceRequirements = append(wireContainer.ResourceRequirements,
			&ecsacs.ResourceRequirement{
				Type:            aws.String("GPU"),
				SharingStrategy: &ecsacs.SharingStrategy{NvidiaMps: &ecsacs.NvidiaMpsAllocation{Memory: aws.Int64(mem)}},
			})
		logger.Info("MOCK: injected MPS sharingStrategy from docker label", logger.Fields{
			field.TaskARN: aws.ToString(acsTask.Arn),
			"container":   aws.ToString(wireContainer.Name),
			"memoryMiB":   mem,
		})
	}
}
