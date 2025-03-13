// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type CheckpointPhase string

const (
	CheckpointInitializing CheckpointPhase = "Initializing"
	CheckpointPending      CheckpointPhase = "Pending"
	Checkpointing          CheckpointPhase = "Checkpointing"
	Checkpointed           CheckpointPhase = "Checkpointed"
	CheckpointMigrating    CheckpointPhase = "Migrating"
	CheckpointMigrated     CheckpointPhase = "Migrated"
	CheckpointFailed       CheckpointPhase = "Failed"
)

type CheckpointSpec struct {
	// PodName is used to specify pod for checkpointing. only pod in the same namespace of Checkpoint will be selected.
	// +required
	PodName string `json:"podName"`
	// Checkpoint result will be stored under this path on the node.
	// If no path is set, default value /data/grit/pods will be used.
	// Moreover, if no enough disk space under this path for storing checkpoint data, checkpoint will fail into failed state.
	// +optional
	HostPath *corev1.HostPathVolumeSource `json:"hostPath,omitempty"`
	// VolumeClaim is used to specify cloud storage for storing checkpoint data and share data across nodes.
	// End user should ensure related pvc/pv resource exist and ready before creating Checkpoint resource.
	// +optional
	VolumeClaim *corev1.PersistentVolumeClaimVolumeSource `json:"volumeClaim,omitempty"`
	// AutoMigration is used for migrating pod across nodes automatically. If true is set, related Restore resource will be created automatically, then checkpointed pod will be deleted by grit-manager, and a new pod will be created automatically by the pod owner(like Deployment and Job). this new pod will be selected as restoration pod and checkpointed data will be used for restoring new pod.
	// This field can be set to true for the following two cases:
	// 1. owner reference of pod is Deployment or Job.
	// 2. VolumeClaim field is specified as a cloud storage, this means checkpointed data can be shared across nodes.
	// +optional
	AutoMigration bool `json:"autoMigration,omitempty"`
}

type CheckpointStatus struct {
	// checkpointed pod is located on this node
	// +optional
	NodeName string `json:"nodeName,omitempty"`
	// PodSpecHash is used for recording hash value of pod spec.
	// Checkpointed data can be used to restore for pod with same hash value.
	// +optional
	PodSpecHash string `json:"podSpecHash,omitempty"`
	// state machine of Checkpoint Phase: Pending --> Checkpointing --> Checkpointed or Failed.
	// +optional
	Phase CheckpointPhase `json:"phase,omitempty"`
	// current state of pod checkpoint
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// checkpointed data is stored under this path in the storage volume. and the data in this path will be used for restoring pod.
	// +optional
	DataPath string `json:"dataPath,omitempty"`
}

// Checkpoint is the Schema for the Checkpoints API
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=checkpoints,scope=Namespaced,categories=girt,shortName={cp,cps}
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Pod",type="string",JSONPath=".spec.podName",description="The pod will be checkpointed"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="The phase of checkpoint action"
// +kubebuilder:printcolumn:name="Storage",type="string",JSONPath=".status.checkpointedDataPath",description="Checkpointed data is stored here"
type Checkpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CheckpointSpec   `json:"spec"`
	Status CheckpointStatus `json:"status,omitempty"`
}

// CheckpointList contains a list of Checkpoint
// +kubebuilder:object:root=true
type CheckpointList struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Items             []Checkpoint `json:"items"`
}
