// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package checkpoint

import (
	"context"
	"fmt"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kaito-project/grit/cmd/grit-agent/app/options"
	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
	"github.com/kaito-project/grit/pkg/gritagent/copy"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers/util"
)

const (
	GritAgent = "grit-agent"
)

// +kubebuilder:rbac:groups=kaito.sh,resources=checkpoints,verbs=get
// +kubebuilder:rbac:groups=kaito.sh,resources=checkpoints/status,verbs=update

func RunCheckpoint(ctx context.Context, opts *options.GritAgentOptions) error {
	// create client
	cfg := ctrl.GetConfigOrDie()
	cfg.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(float32(opts.KubeClientQPS), opts.KubeClientBurst)
	cfg.UserAgent = GritAgent
	kubeClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		return err
	}

	// get checkpoint
	var ckpt v1alpha1.Checkpoint
	if err := kubeClient.Get(ctx, client.ObjectKey{Namespace: opts.TargetPodNamespace, Name: opts.CheckpointName}, &ckpt); err != nil {
		return err
	}

	// execute checkpoint
	if err := RuntimeCheckpointPod(ctx, &opts.RuntimeCheckpointOptions); err != nil {
		ckpt.Status.Phase = v1alpha1.CheckpointFailed
		util.UpdateCondition(&clock.RealClock{}, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "RuntimeCheckpointFailed", fmt.Sprintf("failed to checkpoint by runtime, %v", err))
		return kubeClient.Status().Update(ctx, &ckpt)
	}

	// transfer checkpointed data to cloud storage
	if err := copy.TransferData(opts.SrcDir, opts.DstDir); err != nil {
		ckpt.Status.Phase = v1alpha1.CheckpointFailed
		util.UpdateCondition(&clock.RealClock{}, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "CheckpointDataCopyFailed", fmt.Sprintf("failed to copy checkpoint data to storage, %v", err))
		return kubeClient.Status().Update(ctx, &ckpt)
	}

	ckpt.Status.Phase = v1alpha1.Checkpointed
	ckpt.Status.DataPath = filepath.Join(opts.TargetPodNamespace, opts.CheckpointName)
	util.UpdateCondition(&clock.RealClock{}, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.Checkpointed), "CheckpointAndStoreCompleted", "Pod checkpoint has been completed")
	return kubeClient.Status().Update(ctx, &ckpt)
}
