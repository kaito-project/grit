// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package pod

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
	"github.com/kaito-project/grit/pkg/gritmanager/agentmanager"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers/util"
)

type PodRestoreWebhook struct {
	client.Client
	agentManager *agentmanager.AgentManager
}

func NewWebook(client client.Client, agentManager *agentmanager.AgentManager) *PodRestoreWebhook {
	return &PodRestoreWebhook{
		Client:       client,
		agentManager: agentManager,
	}
}

func (w *PodRestoreWebhook) Default(ctx context.Context, obj runtime.Object) error {
	ctx = util.WithWebhookName(ctx, "pod.restore")
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return fmt.Errorf("expected a Pod object but got a different type")
	}

	// Pod has already been selected as restoration pod, skip it.
	if pod.Annotations != nil && len(pod.Annotations[v1alpha1.CheckpointDataPathLabel]) != 0 {
		return nil
	}

	var restoreList v1alpha1.RestoreList
	if err := w.List(ctx, &restoreList, &client.ListOptions{Namespace: pod.Namespace}); err != nil {
		log.FromContext(ctx).Error(err, "failed to list restore resources", "namespace", pod.Namespace, "podName", pod.Name)
		return nil //nolint:nilerr
	}

	restores := lo.Filter(restoreList.Items, func(restore v1alpha1.Restore, _ int) bool {
		if restore.Status.Phase != v1alpha1.RestorePending {
			return false
		}
		if len(restore.Status.TargetPod) != 0 {
			return false
		}

		return true
	})

	if len(restores) == 0 {
		return nil
	}

	// check there is any Restore can matchi the pod(PodSpecHash and Owner Reference)
	var selectedRestore *v1alpha1.Restore
	podSpecHash := util.ComputeHash(&pod.Spec)
	for i := range restores {
		if restores[i].Annotations[v1alpha1.PodSpecHashLabel] != podSpecHash {
			continue
		}

		for _, ownerRef := range pod.OwnerReferences {
			if ownerRef.UID == restores[i].Spec.OwnerRef.UID &&
				ownerRef.Kind == restores[i].Spec.OwnerRef.Kind &&
				ownerRef.APIVersion == restores[i].Spec.OwnerRef.APIVersion {
				selectedRestore = &restores[i]
				break
			}
		}

		if selectedRestore != nil {
			break
		}
	}

	if selectedRestore == nil {
		return nil
	}

	selectedRestore.Status.TargetPod = pod.Name
	if err := w.Status().Update(ctx, selectedRestore); err != nil {
		log.FromContext(ctx).Error(err, "failed to update target pod for restore", "restore", selectedRestore.Name, "pod", pod.Name)
		return err
	}

	// add annotation for pod
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[v1alpha1.CheckpointDataPathLabel] = filepath.Join(w.agentManager.GetHostPath(), selectedRestore.Namespace, selectedRestore.Spec.CheckpointName)
	pod.Annotations[v1alpha1.RestoreNameLabel] = selectedRestore.Name
	return nil
}

// +kubebuilder:webhook:path=/mutate-core-v1-pod,mutating=true,failurePolicy=ignore,sideEffects=None,admissionReviewVersions=v1,groups="",resources=pods,verbs=create,versions=v1,name=mutate.core.v1.pods.kaito.sh

func (w *PodRestoreWebhook) Register(_ context.Context, mgr manager.Manager) error {
	return controllerruntime.NewWebhookManagedBy(mgr).
		For(&corev1.Pod{}).
		WithDefaulter(w).
		WithCustomPath("/mutate-core-v1-pod").
		Complete()
}
