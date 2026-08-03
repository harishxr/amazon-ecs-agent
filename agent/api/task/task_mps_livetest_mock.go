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
// Section 5 (MPS device assignment) relaxes addGPUResource so that multiple MPS
// containers named by a single GPU association colocate on one physical GPU. On
// a live instance neither input that drives it is available yet:
//
//  1. MPSConfig (which makes UsesMPS() true) comes from the ACS sharingStrategy,
//     which the control plane does not stream yet.
//  2. The multi-container GPU association is normally streamed by CMBS after it
//     places the task; CMBS per-GPU placement is not deployed, so no such
//     association arrives.
//
// This file synthesizes both from docker labels that survive the real
// RegisterTaskDefinition -> ACS path, so the UNCHANGED real Section 5 code runs:
//
//	mps.mock.memory-mib    (required, MiB, > 0)   -> NvidiaMps.Memory
//	mps.mock.compute-pct   (optional, 1-100)      -> NvidiaMps.MaxComputePercent
//	mps.mock.gpu-uuid      (required for colocation) -> the physical GPU UUID all
//	                       containers carrying it share. The task carries NO GPU
//	                       resourceRequirement, so the control plane does not gate
//	                       it; the agent assigns the labeled UUID locally, exactly
//	                       as it would consume a real CMBS association.
//
// Delete this file (and its two calls in TaskFromACS) when ACS regen carries
// sharingStrategy and CMBS streams the per-GPU association for real.

package task

import (
	"encoding/json"
	"strconv"

	apicontainer "github.com/aws/amazon-ecs-agent/agent/api/container"
	"github.com/aws/amazon-ecs-agent/ecs-agent/acs/model/ecsacs"
	"github.com/aws/amazon-ecs-agent/ecs-agent/logger"
	"github.com/aws/amazon-ecs-agent/ecs-agent/logger/field"
	"github.com/aws/aws-sdk-go-v2/aws"
)

const (
	mockMPSMemoryLabel     = "mps.mock.memory-mib"
	mockMPSComputePctLabel = "mps.mock.compute-pct"
	mockMPSGpuUUIDLabel    = "mps.mock.gpu-uuid"
)

// dockerConfigLabels is the subset of the docker "Config" blob we read labels from.
type dockerConfigLabels struct {
	Labels map[string]string `json:"Labels"`
}

// mockInjectMPSSharingStrategyFromLabels appends a real ecsacs GPU
// sharingStrategy resource requirement to each wire container carrying the
// mps.mock.memory-mib label. Runs in TaskFromACS BEFORE
// applyGPUResourceRequirements so the real consume path maps it onto MPSConfig.
func mockInjectMPSSharingStrategyFromLabels(acsTask *ecsacs.Task) {
	for _, wireContainer := range acsTask.Containers {
		if wireContainer == nil {
			continue
		}
		labels := mockLabelsFromDockerConfig(wireContainer.DockerConfig)
		memStr, ok := labels[mockMPSMemoryLabel]
		if !ok {
			continue
		}
		mem, err := strconv.ParseInt(memStr, 10, 64)
		if err != nil || mem <= 0 {
			continue
		}
		alloc := &ecsacs.NvidiaMpsAllocation{Memory: aws.Int64(mem)}
		if pctStr, ok := labels[mockMPSComputePctLabel]; ok {
			if pct, err := strconv.ParseInt(pctStr, 10, 64); err == nil {
				alloc.MaxComputePercent = aws.Int64(pct)
			}
		}
		wireContainer.ResourceRequirements = append(wireContainer.ResourceRequirements,
			&ecsacs.ResourceRequirement{
				Type:            aws.String("GPU"),
				SharingStrategy: &ecsacs.SharingStrategy{NvidiaMps: alloc},
			})
		logger.Info("MOCK: injected MPS sharingStrategy from docker label", logger.Fields{
			field.TaskARN: aws.ToString(acsTask.Arn),
			"container":   aws.ToString(wireContainer.Name),
			"memoryMiB":   mem,
		})
	}
}

// mockInjectPinnedGPUAssociationFromLabels synthesizes the multi-container GPU
// association CMBS would stream, from the mps.mock.gpu-uuid label. Containers
// carrying the same UUID label are grouped into one Association{Type:gpu,
// Name:<uuid>, Containers:[...]}. Runs in TaskFromACS AFTER
// applyGPUResourceRequirements (so MPSConfig/UsesMPS is set) and before
// addGPUResource consumes task.Associations. A UUID that already has a real
// control-plane association is left untouched.
func mockInjectPinnedGPUAssociationFromLabels(task *Task) {
	var order []string
	byUUID := map[string][]string{}
	for _, container := range task.Containers {
		labels := mockLabelsFromInternalContainer(container)
		uuid, ok := labels[mockMPSGpuUUIDLabel]
		if !ok || uuid == "" {
			continue
		}
		if _, seen := byUUID[uuid]; !seen {
			order = append(order, uuid)
		}
		byUUID[uuid] = append(byUUID[uuid], container.Name)
	}
	existing := map[string]bool{}
	for _, a := range task.Associations {
		if a.Type == GPUAssociationType {
			existing[a.Name] = true
		}
	}
	for _, uuid := range order {
		if existing[uuid] {
			continue
		}
		task.Associations = append(task.Associations, Association{
			Containers: byUUID[uuid],
			Name:       uuid,
			Type:       GPUAssociationType,
		})
		logger.Info("MOCK: injected pinned GPU association from docker label", logger.Fields{
			field.TaskARN: task.Arn,
			"gpuUUID":     uuid,
			"containers":  byUUID[uuid],
		})
	}
}

func mockLabelsFromDockerConfig(dc *ecsacs.DockerConfig) map[string]string {
	if dc == nil || dc.Config == nil {
		return map[string]string{}
	}
	return mockParseLabels(aws.ToString(dc.Config))
}

func mockLabelsFromInternalContainer(c *apicontainer.Container) map[string]string {
	if c.DockerConfig.Config == nil {
		return map[string]string{}
	}
	return mockParseLabels(aws.ToString(c.DockerConfig.Config))
}

func mockParseLabels(configJSON string) map[string]string {
	var cfg dockerConfigLabels
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil || cfg.Labels == nil {
		return map[string]string{}
	}
	return cfg.Labels
}
