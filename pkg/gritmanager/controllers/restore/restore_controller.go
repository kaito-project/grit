// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package restore

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/samber/lo"
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
		string(v1alpha1.RestoreCreated): 1,
		string(v1alpha1.RestorePending): 2,
		string(v1alpha1.Restoring):      3,
		string(v1alpha1.Restored):       4,
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

	c.statesMachine = map[v1alpha1.RestorePhase]RestoreStateHandler{
		v1alpha1.RestoreCreated: c.createdHandler,
		v1alpha1.RestorePending: c.pendingHandler,
		v1alpha1.Restoring:      c.restoringHandler,
		v1alpha1.Restored:       c.restoredHandler,
	}

	return c
}

func (c *Controller) Reconcile(ctx context.Context, restore *v1alpha1.Restore) (reconcile.Result, error) {
	ctx = util.WithControllerName(ctx, "restore.lifecycle")

	updatedRestore := restore.DeepCopy()
	phase := v1alpha1.RestorePhase(util.ResolveLastPhaseFromConditions(updatedRestore.Status.Conditions, restoreConditionOrder, string(v1alpha1.RestoreCreated)))
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

// createdHandler is used for waiting to select the restoration pod, then upgraded state to RestorePending.
func (c *Controller) createdHandler(ctx context.Context, restore *v1alpha1.Restore) error {
	if restore.Status.Phase == "" {
		restore.Status.Phase = v1alpha1.RestoreCreated
		util.UpdateCondition(c.clock, &restore.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.RestoreCreated), "RestoreIsCreated", "restore resource is created")
		return nil
	}

	// waiting restoration pod is selected
	if restore.Annotations[v1alpha1.RestorationPodSelectedLabel] != "true" {
		return nil
	}

	var podList corev1.PodList
	if err := c.List(ctx, &podList, &client.ListOptions{Namespace: restore.Namespace}); err != nil {
		return err
	}

	pods := lo.Filter(podList.Items, func(pod corev1.Pod, _ int) bool {
		return pod.Annotations[v1alpha1.RestoreNameLabel] == restore.Name
	})

	if len(pods) == 0 {
		return fmt.Errorf("there is no pod for selected restore(%s), wait pod created", restore.Name)
	} else if len(pods) > 1 {
		restore.Status.Phase = v1alpha1.RestoreFailed
		util.UpdateCondition(c.clock, &restore.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.RestoreFailed), "MultiplePodsSelected", fmt.Sprintf("%d pods are selected as restoration pod for restore(%s)", len(pods), restore.Name))
		return nil
	}

	if len(pods[0].Spec.NodeName) != 0 {
		restore.Status.NodeName = pods[0].Spec.NodeName
	}
	restore.Status.TargetPod = pods[0].Name
	restore.Status.Phase = v1alpha1.RestorePending
	util.UpdateCondition(c.clock, &restore.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.RestorePending), "RestorationPodSelected", fmt.Sprintf("pod(%s) is selected as a restoration pod", pods[0].Name))
	return nil
}

// pendingHandler is used for distributing grit agent pod to specified node which has the pod for restoring.
// restore state will be upgraded to restoring after grit agent pod created.
func (c *Controller) pendingHandler(ctx context.Context, restore *v1alpha1.Restore) error {
	// Target pod is selected
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
	if err := c.Get(ctx, client.ObjectKey{Namespace: restore.Namespace, Name: util.GritAgentJobName(nil, restore)}, &job); err == nil {
		restore.Status.Phase = v1alpha1.Restoring
		util.UpdateCondition(c.clock, &restore.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.Restoring), "GritAgentIsCreated", fmt.Sprintf("grit agent job(%s/%s) for restore is created", job.Namespace, job.Name))
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

// restoringHandler is used for checking restoration pod is restored or not.
func (c *Controller) restoringHandler(ctx context.Context, restore *v1alpha1.Restore) error {
	var restorationPod corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: restore.Namespace, Name: restore.Status.TargetPod}, &restorationPod); client.IgnoreNotFound(err) != nil {
		return err
	} else if err != nil { // pod not found error
		restore.Status.Phase = v1alpha1.RestoreFailed
		util.UpdateCondition(c.clock, &restore.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.RestoreFailed), "RestorationPodNotFound", fmt.Sprintf("failed to find restoration pod(%s) for restore(%s), %v", restore.Status.TargetPod, restore.Name, err))
		return nil
	}

	if restorationPod.Status.Phase == corev1.PodFailed {
		restore.Status.Phase = v1alpha1.RestoreFailed
		util.UpdateCondition(c.clock, &restore.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.RestoreFailed), "RestorationPodFailed", fmt.Sprintf("restoration pod(%s) for restore(%s) failed to start", restore.Status.TargetPod, restore.Name))
	} else if restorationPod.Status.Phase == corev1.PodRunning {
		restore.Status.Phase = v1alpha1.Restored
		util.UpdateCondition(c.clock, &restore.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.Restored), "RestorationPodRunning", fmt.Sprintf("restoration pod(%s) for restore(%s) is running", restore.Status.TargetPod, restore.Name))
	}

	return nil
}

// restoredHandler is used for garbage collecting grit agent pod which used for restoring pod.
func (c *Controller) restoredHandler(ctx context.Context, restore *v1alpha1.Restore) error {
	var gritAgentJob batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: restore.Namespace, Name: util.GritAgentJobName(nil, restore)}, &gritAgentJob); err == nil {
		if gritAgentJob.DeletionTimestamp.IsZero() {
			deletePolicy := metav1.DeletePropagationForeground
			return c.Delete(ctx, &gritAgentJob, &client.DeleteOptions{PropagationPolicy: &deletePolicy})
		}
	} else if client.IgnoreNotFound(err) != nil {
		return err
	}

	// grit agent job has been removed.
	return nil
}

// +kubebuilder:rbac:groups=kaito.sh,resources=restores,verbs=list;watch;get
// +kubebuilder:rbac:groups=kaito.sh,resources=restores/status,verbs=update
// +kubebuilder:rbac:groups=kaito.sh,resources=checkpoints,verbs=get
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=list;watch;get;create;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=list;watch

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("restore.lifecycle").
		For(&v1alpha1.Restore{}).
		Watches(&batchv1.Job{}, util.GritAgentJobHandler, builder.WithPredicates(util.GritAgentJobPredicate)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return []reconcile.Request{}
			}

			if restoreName, ok := pod.Annotations[v1alpha1.RestoreNameLabel]; ok {
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
