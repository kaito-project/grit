// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type RestorePhase string

const (
	RestoreNone    RestorePhase = "none"
	RestorePending RestorePhase = "Pending"
	Restoring      RestorePhase = "Restoring"
	Restored       RestorePhase = "Restored"
	RestoreFailed  RestorePhase = "Failed"
)

type RestoreSpec struct {
	// CheckpointName is used to specify Checkpoint resource. only Checkpoint in the same namespace of Restore will be selected.
	// Only checkpointed Checkpoint will be accepted, and checkpointed data will be used for restoring pod.
	// +required
	CheckpointName string `json:"checkpointName"`
	// Pod will be selected as target pod for restoring with following conditions:
	// 1. pod has labels which match this selector
	// 2. pod spec has the same hash value corresponding to Checkpoint.
	// +required
	Selector *metav1.LabelSelector `json:"selector"`
}

type RestoreStatus struct {
	// restoration pod is located on this node
	// +optional
	NodeName string `json:"nodeName,omitempty"`
	// the pod specified by TargetPod is selected for restoring.
	// +optional
	TargetPod string `json:"targetPod,omitempty"`
	// state machine of Restore Phase: Pending --> Restoring --> Restored or Failed.
	// +optional
	Phase RestorePhase `json:"phase,omitempty"`
	// current state of pod restore
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Restore is the Schema for the Restores API
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=restores,scope=Namespaced,categories=girt,shortName=rt
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Checkpoint",type="string",JSONPath=".spec.checkpointName",description="The data of the checkpoint will be used for restoring"
// +kubebuilder:printcolumn:name="RestorationPod",type="string",JSONPath=".status.restorationPod",description="The pod will be restored"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="The phase of restore action"
type Restore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RestoreSpec   `json:"spec"`
	Status            RestoreStatus `json:"status,omitempty"`
}

// RestoreList contains a list of Restore
// +kubebuilder:object:root=true
type RestoreList struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Items             []Restore `json:"items"`
}
