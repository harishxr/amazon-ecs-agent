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
	"errors"
	"strings"

	resourcestatus "github.com/aws/amazon-ecs-agent/agent/taskresource/status"
)

// MPSDaemonStatus defines the resource statuses for the MPS daemon health gate.
type MPSDaemonStatus resourcestatus.ResourceStatus

const (
	// MPSDaemonStatusNone is the zero state of the resource.
	MPSDaemonStatusNone MPSDaemonStatus = iota
	// MPSDaemonVerified is the state where the MPS control daemon has been
	// verified as functionally serving (the steady/created state).
	MPSDaemonVerified
	// MPSDaemonRemoved is the terminal (cleaned-up) state.
	MPSDaemonRemoved
)

var mpsDaemonStatusMap = map[string]MPSDaemonStatus{
	"NONE":     MPSDaemonStatusNone,
	"VERIFIED": MPSDaemonVerified,
	"REMOVED":  MPSDaemonRemoved,
}

// String returns a human readable string representation of the status.
func (s MPSDaemonStatus) String() string {
	for str, status := range mpsDaemonStatusMap {
		if status == s {
			return str
		}
	}
	return "NONE"
}

// MarshalJSON overrides the logic for JSON-encoding the status.
func (s *MPSDaemonStatus) MarshalJSON() ([]byte, error) {
	if s == nil {
		return nil, nil
	}
	return []byte(`"` + s.String() + `"`), nil
}

// UnmarshalJSON overrides the logic for parsing the JSON-encoded status.
func (s *MPSDaemonStatus) UnmarshalJSON(b []byte) error {
	if strings.ToLower(string(b)) == "null" {
		*s = MPSDaemonStatusNone
		return nil
	}
	if len(b) < 2 || b[0] != '"' || b[len(b)-1] != '"' {
		*s = MPSDaemonStatusNone
		return errors.New("mps daemon status unmarshal: status must be a string or null; got " + string(b))
	}
	status, ok := mpsDaemonStatusMap[string(b[1:len(b)-1])]
	if !ok {
		*s = MPSDaemonStatusNone
		return errors.New("mps daemon status unmarshal: unrecognized status " + string(b))
	}
	*s = status
	return nil
}
