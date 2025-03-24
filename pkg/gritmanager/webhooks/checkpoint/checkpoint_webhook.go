// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package checkpoint

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers/util"
)

type CheckpointWebhook struct {
	client.Client
}

func NewCheckpointWebhook(client client.Client) *CheckpointWebhook {
	return &CheckpointWebhook{Client: client}
}

func (w *CheckpointWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	ctx = util.WithWebhookName(ctx, "checkpoint.validate")
	ckpt, ok := obj.(*v1alpha1.Checkpoint)
	if !ok {
		return admission.Warnings{}, fmt.Errorf("expected a checkpoint object but got a different type")
	}

	if len(ckpt.Spec.PodName) == 0 {
		return admission.Warnings{}, fmt.Errorf("pod is not specified in checkpoint(%s)", ckpt.Name)
	}

	var pod corev1.Pod
	if err := w.Get(ctx, client.ObjectKey{Namespace: ckpt.Namespace, Name: ckpt.Spec.PodName}, &pod); err != nil {
		return admission.Warnings{}, err
	}

	// related pod resource should be running
	if pod.Status.Phase != corev1.PodRunning || len(pod.Spec.NodeName) == 0 {
		return admission.Warnings{}, fmt.Errorf("pod(%s) referenced by chekcpoint(%s) is not running", pod.Name, ckpt.Name)
	}

	var node corev1.Node
	if err := w.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, &node); err != nil {
		return admission.Warnings{}, err
	}

	// pod related node should be ready
	if !isNodeReady(&node) {
		return admission.Warnings{}, fmt.Errorf("node(%s) referenced by pod(%s) and checkpoint(%s) is not ready", node.Name, pod.Name, ckpt.Name)
	}

	return admission.Warnings{}, nil
}

func (w *CheckpointWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	return admission.Warnings{}, nil
}

func (w *CheckpointWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	return admission.Warnings{}, nil
}

func isNodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}

	return false
}

// +kubebuilder:webhook:path=/validate-kaito-sh-v1alpha1-checkpoint,mutating=false,failurePolicy=fail,sideEffects=None,admissionReviewVersions=v1,groups="kaito.sh",resources=checkpoints,verbs=create,versions=v1alpha1,name=validate.kaito.sh.v1alpha1.restore.grit
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get

func (w *CheckpointWebhook) Register(_ context.Context, mgr manager.Manager) error {
	return controllerruntime.NewWebhookManagedBy(mgr).
		For(&v1alpha1.Checkpoint{}).
		WithValidator(w).
		Complete()
}
