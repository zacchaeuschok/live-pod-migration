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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type PodMigrationPhase string

const (
	MigrationPhasePending            PodMigrationPhase = "Pending"
	MigrationPhaseCheckpointing      PodMigrationPhase = "Checkpointing" 
	MigrationPhaseCheckpointComplete PodMigrationPhase = "CheckpointComplete"
	MigrationPhaseRestoring          PodMigrationPhase = "Restoring"
	MigrationPhaseSucceeded          PodMigrationPhase = "Succeeded"
	MigrationPhaseFailed             PodMigrationPhase = "Failed"
)

// PodMigrationSpec defines the desired state of PodMigration.
type PodMigrationSpec struct {
	// Name of the Pod to migrate (required).
	PodName string `json:"podName"`

	// TargetNode is the name of the node where the Pod should be restored.
	TargetNode string `json:"targetNode"`
}

// PodMigrationStatus defines the observed state of PodMigration.
type PodMigrationStatus struct {
	// Phase is the high-level lifecycle marker.
	Phase PodMigrationPhase `json:"phase,omitempty"`

	// Message is a human-readable summary of the most recent state transition
	// or error.
	Message string `json:"message,omitempty"`

	// PodCheckpointRef lets PodMigration track the checkpoint it spawned/bound.
	PodCheckpointRef *corev1.LocalObjectReference `json:"podCheckpointRef,omitempty"`
	
	// RestoredPodName is the name of the restored pod after migration.
	RestoredPodName string `json:"restoredPodName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// PodMigration is the Schema for the podmigrations API.
type PodMigration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodMigrationSpec   `json:"spec,omitempty"`
	Status PodMigrationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PodMigrationList contains a list of PodMigration.
type PodMigrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodMigration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PodMigration{}, &PodMigrationList{})
}
