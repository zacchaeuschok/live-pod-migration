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

// ContainerCheckpointContentSpec defines the desired state of ContainerCheckpointContent.
type ContainerCheckpointContentSpec struct {
	// Backref to the namespaced ContainerCheckpoint this content satisfies.
	ContainerCheckpointRef corev1.ObjectReference `json:"containerCheckpointRef"`

	// Redundant but convenient pod/container identity.
	PodNamespace  string `json:"podNamespace"`
	PodName       string `json:"podName"`
	ContainerName string `json:"containerName"`

	// ArtifactURI: path/URI to the checkpoint bundle (runtime-defined).
	ArtifactURI string `json:"artifactURI"`
}

// ContainerCheckpointContentStatus defines the observed state of ContainerCheckpointContent.

type ContainerCheckpointContentStatus struct {
	Ready        bool         `json:"ready,omitempty"`
	Message      string       `json:"message,omitempty"`
	CreationTime *metav1.Time `json:"creationTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// ContainerCheckpointContent is the Schema for the containercheckpointcontents API.
type ContainerCheckpointContent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ContainerCheckpointContentSpec   `json:"spec,omitempty"`
	Status ContainerCheckpointContentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ContainerCheckpointContentList contains a list of ContainerCheckpointContent.
type ContainerCheckpointContentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ContainerCheckpointContent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ContainerCheckpointContent{}, &ContainerCheckpointContentList{})
}
