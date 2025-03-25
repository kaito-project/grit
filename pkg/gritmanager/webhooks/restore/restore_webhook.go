// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package restore

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers/util"
)

type RestoreWebhook struct {
	client.Client
}

func NewRestoreWebhook(client client.Client) *RestoreWebhook {
	return &RestoreWebhook{Client: client}
}

func (w *RestoreWebhook) Default(ctx context.Context, obj runtime.Object) error {
	ctx = util.WithWebhookName(ctx, "restore.default")
	restore, ok := obj.(*v1alpha1.Restore)
	if !ok {
		return fmt.Errorf("expected a restore object but got a different type")
	}

	var ckpt v1alpha1.Checkpoint
	if err := w.Get(ctx, client.ObjectKey{Namespace: restore.Namespace, Name: restore.Spec.CheckpointName}, &ckpt); err != nil {
		return err
	}

	if restore.Annotations == nil {
		restore.Annotations = make(map[string]string)
	}

	restore.Annotations[v1alpha1.PodSpecHashLabel] = ckpt.Status.PodSpecHash

	return nil
}

func (w *RestoreWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	ctx = util.WithWebhookName(ctx, "restore.validate")
	restore, ok := obj.(*v1alpha1.Restore)
	if !ok {
		return admission.Warnings{}, fmt.Errorf("expected a restore object but got a different type")
	}

	if len(restore.Spec.CheckpointName) == 0 {
		return admission.Warnings{}, fmt.Errorf("checkpoint is not specified in restore(%s)", restore.Name)
	}

	var ckpt v1alpha1.Checkpoint
	if err := w.Get(ctx, client.ObjectKey{Namespace: restore.Namespace, Name: restore.Spec.CheckpointName}, &ckpt); err != nil {
		return admission.Warnings{}, err
	}

	// related checkpoint resource should has completed checkpoint process
	if ckpt.Status.Phase != v1alpha1.Checkpointed &&
		ckpt.Status.Phase != v1alpha1.CheckpointMigrating &&
		ckpt.Status.Phase != v1alpha1.CheckpointMigrated {
		return admission.Warnings{}, fmt.Errorf("restore(%s) referenced checkpoint(%s) has not completed checkpoint process", restore.Name, ckpt.Name)
	}

	return admission.Warnings{}, nil
}

func (w *RestoreWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	return admission.Warnings{}, nil
}

func (w *RestoreWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	return admission.Warnings{}, nil
}

// +kubebuilder:webhook:path=/mutate-kaito-sh-v1alpha1-restore,mutating=true,failurePolicy=fail,sideEffects=None,admissionReviewVersions=v1,groups="kaito.sh",resources=restores,verbs=create,versions=v1alpha1,name=mutating.restores.kaito.sh
// +kubebuilder:webhook:path=/validate-kaito-sh-v1alpha1-restore,mutating=false,failurePolicy=fail,sideEffects=None,admissionReviewVersions=v1,groups="kaito.sh",resources=restores,verbs=create,versions=v1alpha1,name=validating.restores.kaito.sh

func (w *RestoreWebhook) Register(_ context.Context, mgr manager.Manager) error {
	return controllerruntime.NewWebhookManagedBy(mgr).
		For(&v1alpha1.Restore{}).
		WithDefaulter(w).
		WithValidator(w).
		Complete()
}
