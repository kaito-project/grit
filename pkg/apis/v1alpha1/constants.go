// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package v1alpha1

const (
	// label key and value for grit agent job
	GritAgentLabel = "grit.dev/helper"
	GritAgentName  = "grit-agent"

	// annotations for restoration pod
	CheckpointDataPathLabel = "grit.dev/checkpoint"
	RestoreNameLabel        = "grit.dev/restore-name"

	// annotations for restore resource
	PodSpecHashLabel            = "grit.dev/pod-spec-hash"
	RestorationPodSelectedLabel = "grit.dev/pod-selected"
)
