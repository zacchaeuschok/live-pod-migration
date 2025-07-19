/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type PodCheckpointPhase string

const (
	PodCheckpointPhasePending   PodCheckpointPhase = "Pending"
	PodCheckpointPhaseRunning   PodCheckpointPhase = "Running"
	PodCheckpointPhaseSucceeded PodCheckpointPhase = "Succeeded"
	PodCheckpointPhaseFailed    PodCheckpointPhase = "Failed"
)

// PodCheckpointSpec defines the desired state of PodCheckpoint.
type PodCheckpointSpec struct {
	PodName *string `json:"podName"`
}

// PodCheckpointStatus defines the observed state of PodCheckpoint.
type PodCheckpointStatus struct {
	Phase   PodCheckpointPhase `json:"phase,omitempty"`
	Message string             `json:"message,omitempty"`
	Ready   bool               `json:"ready,omitempty"`

	// BoundContentName names the PodCheckpointContent (cluster-scoped) that
	// materializes this checkpoint. Empty until bound.
	BoundContentName string `json:"boundContentName,omitempty"`

	CreationTime   *metav1.Time `json:"creationTime,omitempty"`   // when checkpoint captured
	CompletionTime *metav1.Time `json:"completionTime,omitempty"` // when phase terminal
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// PodCheckpoint is the Schema for the podcheckpoints API.
type PodCheckpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodCheckpointSpec   `json:"spec,omitempty"`
	Status PodCheckpointStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PodCheckpointList contains a list of PodCheckpoint.
type PodCheckpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodCheckpoint `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PodCheckpoint{}, &PodCheckpointList{})
}
