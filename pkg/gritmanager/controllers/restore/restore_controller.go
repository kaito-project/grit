// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package restore

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"golang.org/x/time/rate"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
	"github.com/kaito-project/grit/pkg/gritmanager/agentmanager"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers/util"
)

var (
	restoreConditionOrder = map[string]int{
		string(v1alpha1.RestorePending): 1,
		string(v1alpha1.Restoring):      2,
		string(v1alpha1.Restored):       3,
	}
)

type RestoreStateHandler func(ctx context.Context, restore *v1alpha1.Restore) error

type Controller struct {
	client.Client
	clock         clock.Clock
	agentManager  *agentmanager.AgentManager
	statesMachine map[v1alpha1.RestorePhase]RestoreStateHandler
}

func NewController(clk clock.Clock, kubeClient client.Client, agentManager *agentmanager.AgentManager) *Controller {
	c := &Controller{
		clock:        clk,
		Client:       kubeClient,
		agentManager: agentManager,
	}

	// At v1alpha1.Restoring state, girt-manager don't need to do anything.
	c.statesMachine = map[v1alpha1.RestorePhase]RestoreStateHandler{
		v1alpha1.RestoreNone:    c.initializingHandler,
		v1alpha1.RestorePending: c.pendingHandler,
		v1alpha1.Restored:       c.restoredHandler,
	}

	return c
}

func (c *Controller) Reconcile(ctx context.Context, restore *v1alpha1.Restore) (reconcile.Result, error) {
	ctx = util.WithControllerName(ctx, "restore.lifecycle")

	updatedRestore := restore.DeepCopy()
	phase := c.resolveLastPhase(updatedRestore)
	log.FromContext(ctx).Info("the last pahse of restore", "namespace", restore.Namespace, "restore", restore.Name, "phase", phase)
	stateHandler, ok := c.statesMachine[phase]
	if !ok {
		return reconcile.Result{}, nil
	}

	if err := stateHandler(ctx, updatedRestore); err != nil {
		return reconcile.Result{}, err
	}

	// if phase is not RestoreFailed, we need to remove failed condition
	if updatedRestore.Status.Phase != v1alpha1.RestoreFailed {
		util.RemoveCondition(&updatedRestore.Status.Conditions, string(v1alpha1.CheckpointFailed))
	}

	if !reflect.DeepEqual(restore, updatedRestore) {
		return reconcile.Result{}, c.Status().Update(ctx, updatedRestore)
	}
	return reconcile.Result{}, nil
}

// resolveLastPhase is used for getting the last phase before failed, so state machine can move out of failed state if
// errors have been fixed.
func (c *Controller) resolveLastPhase(restore *v1alpha1.Restore) v1alpha1.RestorePhase {
	phase := restore.Status.Phase
	if phase == "" {
		phase = v1alpha1.RestoreNone
		return phase
	} else if phase != v1alpha1.RestoreFailed {
		return phase
	}

	// if phase is RestoreFailed, we need to resolve conditions and find the last phase before failed.
	maxOrder := -1
	for _, cond := range restore.Status.Conditions {
		if order, exists := restoreConditionOrder[cond.Type]; exists && order > maxOrder {
			maxOrder = order
			phase = v1alpha1.RestorePhase(cond.Type)
		}
	}

	// if there is no conditions, we should fall back to beginning.
	if phase == v1alpha1.RestoreFailed {
		phase = v1alpha1.RestoreNone
	}
	return phase
}

// initializingHandler is used for initializing restore resource, then upgraded state to RestorePending.
func (c *Controller) initializingHandler(ctx context.Context, restore *v1alpha1.Restore) error {
	restore.Status.Phase = v1alpha1.RestorePending
	util.UpdateCondition(c.clock, &restore.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.RestorePending), "InitializingCompleted", "initialize restore state to pending")
	return nil
}

// pendingHandler is used for distributing grit agent pod to specified node which has the pod for restoring.
// restore state will be upgraded to restoring after grit agent pod becomes running.
func (c *Controller) pendingHandler(ctx context.Context, restore *v1alpha1.Restore) error {
	// waiting restoration pod is selected
	if len(restore.Status.TargetPod) == 0 {
		return nil
	}

	// waiting target pod is scheduled
	if len(restore.Status.NodeName) == 0 {
		var pod corev1.Pod
		if err := c.Get(ctx, client.ObjectKey{Namespace: restore.Namespace, Name: restore.Status.TargetPod}, &pod); err == nil {
			if len(pod.Spec.NodeName) != 0 {
				restore.Status.NodeName = pod.Spec.NodeName
			}
			return nil
		} else if apierrors.IsNotFound(err) {
			restore.Status.Phase = v1alpha1.RestoreFailed
			util.UpdateCondition(c.clock, &restore.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.RestoreFailed), "TargetPodNotExist", fmt.Sprintf("target pod(%s) for restore(%s) doesn't exist", restore.Status.TargetPod, restore.Name))
			return nil
		} else {
			return err
		}
	}

	// grit agent job is running, upgrade state to checkpointing when job is ready
	var job batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: restore.Namespace, Name: restore.Name}, &job); err == nil {
		if job.Status.Ready != nil && *(job.Status.Ready) == 1 {
			restore.Status.Phase = v1alpha1.Restoring
			util.UpdateCondition(c.clock, &restore.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.Restoring), "GritAgentIsReady", fmt.Sprintf("grit agent pod(%s/%s) is ready", job.Namespace, job.Name))
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	// grit agent doesn't exist, create a grit agent job based on restore and checkpoint.
	var ckpt v1alpha1.Checkpoint
	if err := c.Get(ctx, client.ObjectKey{Namespace: restore.Namespace, Name: restore.Spec.CheckpointName}, &ckpt); err != nil {
		if apierrors.IsNotFound(err) {
			restore.Status.Phase = v1alpha1.RestoreFailed
			util.UpdateCondition(c.clock, &restore.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.RestoreFailed), "CheckpointNotExist", fmt.Sprintf("checkpoint(%s/%s) which is used for restore(%s) doesn't exist", restore.Namespace, restore.Spec.CheckpointName, restore.Name))
			return nil
		}
		return err
	}

	gritAgentJob, err := c.agentManager.GenerateGritAgentJob(ctx, &ckpt, restore)
	if err != nil {
		restore.Status.Phase = v1alpha1.RestoreFailed
		util.UpdateCondition(c.clock, &restore.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.RestoreFailed), "GenerateGritAgentFailed", fmt.Sprintf("failed to generate grit agent job, %v", err))
		return nil
	}

	// start to distribute grit agent job
	return c.Create(ctx, gritAgentJob)
}

// restoredHandler is used for garbage collecting grit agent pod which used for restoring pod.
func (c *Controller) restoredHandler(ctx context.Context, restore *v1alpha1.Restore) error {
	var gritAgentJob batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: restore.Namespace, Name: restore.Name}, &gritAgentJob); err == nil {
		if gritAgentJob.DeletionTimestamp.IsZero() {
			return c.Delete(ctx, &gritAgentJob)
		}
	} else if client.IgnoreNotFound(err) != nil {
		return err
	}

	// grit agent job has been removed.
	return nil
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("restore.lifecycle").
		For(&v1alpha1.Restore{}).
		Watches(&batchv1.Job{}, &handler.EnqueueRequestForObject{}, builder.WithPredicates(util.GritAgentJobPredicate)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return []reconcile.Request{}
			}

			if restoreName, ok := pod.Labels[v1alpha1.RestoreNameLabel]; ok {
				return []reconcile.Request{
					{
						NamespacedName: types.NamespacedName{Namespace: pod.Namespace, Name: restoreName},
					},
				}
			}
			return []reconcile.Request{}
		}), builder.WithPredicates(util.RestorationPodPredicate)).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Second, 300*time.Second),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
			MaxConcurrentReconciles: 5,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
