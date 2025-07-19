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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PodCheckpointContentSpec defines the desired state of PodCheckpointContent.
type PodCheckpointContentSpec struct {
	// PodCheckpointRef: namespaced backref to the PodCheckpoint this content binds to.
	// Both name and namespace must be set for a valid bind.
	PodCheckpointRef corev1.ObjectReference `json:"podCheckpointRef"`

	// PodNamespace / PodName captured for convenience (duplicate of ref target; aids querying).
	PodNamespace string `json:"podNamespace"`
	PodName      string `json:"podName"`

	// ContainerContents: list of cluster-scoped ContainerCheckpointContent object names
	// (kind is implied; group/version same API group).
	ContainerContents []corev1.LocalObjectReference `json:"containerContents"`
}

// PodCheckpointContentStatus defines the observed state of PodCheckpointContent.
type PodCheckpointContentStatus struct {
	Ready        bool         `json:"ready"`
	Message      string       `json:"message,omitempty"`
	CreationTime *metav1.Time `json:"creationTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced

// PodCheckpointContent is the Schema for the podcheckpointcontents API.
type PodCheckpointContent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodCheckpointContentSpec   `json:"spec,omitempty"`
	Status PodCheckpointContentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PodCheckpointContentList contains a list of PodCheckpointContent.
type PodCheckpointContentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodCheckpointContent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PodCheckpointContent{}, &PodCheckpointContentList{})
}
