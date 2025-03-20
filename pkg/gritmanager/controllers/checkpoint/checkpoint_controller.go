// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package checkpoint

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
	checkpointConditionOrder = map[string]int{
		string(v1alpha1.CheckpointPending):   1,
		string(v1alpha1.Checkpointing):       2,
		string(v1alpha1.Checkpointed):        3,
		string(v1alpha1.CheckpointMigrating): 4,
		string(v1alpha1.CheckpointMigrated):  5,
	}
)

type CheckpointStateHandler func(ctx context.Context, ckpt *v1alpha1.Checkpoint) error

type Controller struct {
	client.Client
	clock         clock.Clock
	agentManager  *agentmanager.AgentManager
	statesMachine map[v1alpha1.CheckpointPhase]CheckpointStateHandler
}

func NewController(clk clock.Clock, kubeClient client.Client, agentManager *agentmanager.AgentManager) *Controller {
	c := &Controller{
		clock:        clk,
		Client:       kubeClient,
		agentManager: agentManager,
	}

	// v1alpha1.Checkpointing, v1alpha1.CheckpointFailed, v1alpha1.CheckpointMigrated,
	// these three states, girt-manager don't need to do anything.
	c.statesMachine = map[v1alpha1.CheckpointPhase]CheckpointStateHandler{
		v1alpha1.CheckpointNone:      c.initializingHandler,
		v1alpha1.CheckpointPending:   c.pendingHandler,
		v1alpha1.Checkpointed:        c.checkpointedHandler,
		v1alpha1.CheckpointMigrating: c.migratingHandler,
	}

	return c
}

func (c *Controller) Reconcile(ctx context.Context, ckpt *v1alpha1.Checkpoint) (reconcile.Result, error) {
	ctx = util.WithControllerName(ctx, "checkpoint.lifecycle")

	updatedCkpt := ckpt.DeepCopy()
	phase := c.resolveLastPhase(updatedCkpt)
	log.FromContext(ctx).Info("the last pahse of checkpoint", "namespace", ckpt.Namespace, "checkpoint", ckpt.Name, "phase", phase)
	stateHandler, ok := c.statesMachine[phase]
	if !ok {
		return reconcile.Result{}, nil
	}

	if err := stateHandler(ctx, updatedCkpt); err != nil {
		return reconcile.Result{}, err
	}

	// if phase is not CheckpointFailed, we need to remove failed condition
	if updatedCkpt.Status.Phase != v1alpha1.CheckpointFailed {
		util.RemoveCondition(&updatedCkpt.Status.Conditions, string(v1alpha1.CheckpointFailed))
	}

	if !reflect.DeepEqual(ckpt, updatedCkpt) {
		return reconcile.Result{}, c.Status().Update(ctx, updatedCkpt)
	}
	return reconcile.Result{}, nil
}

// resolveLastPhase is used for getting the last phase before failed, so state machine can move out of failed state if
// errors have been fixed.
func (c *Controller) resolveLastPhase(ckpt *v1alpha1.Checkpoint) v1alpha1.CheckpointPhase {
	phase := ckpt.Status.Phase
	if phase == "" {
		phase = v1alpha1.CheckpointNone
		return phase
	} else if phase != v1alpha1.CheckpointFailed {
		return phase
	}

	// if phase is CheckpointFailed, we need to resolve conditions and find the last phase before failed.
	maxOrder := -1
	for _, cond := range ckpt.Status.Conditions {
		if order, exists := checkpointConditionOrder[cond.Type]; exists && order > maxOrder {
			maxOrder = order
			phase = v1alpha1.CheckpointPhase(cond.Type)
		}
	}

	// if there is no conditions, we should fall back to the beginning.
	if phase == v1alpha1.CheckpointFailed {
		phase = v1alpha1.CheckpointNone
	}
	return phase
}

// initializingHandler is used for initializing pod spec hash for checkpoint resource, then upgraded state to CheckpointPending.
func (c *Controller) initializingHandler(ctx context.Context, ckpt *v1alpha1.Checkpoint) error {
	var pod corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: ckpt.Namespace, Name: ckpt.Spec.PodName}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			ckpt.Status.Phase = v1alpha1.CheckpointFailed
			util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "PodNotExist", fmt.Sprintf("pod(%s) for checkpoint doesn't exist", ckpt.Spec.PodName))
			return nil
		}
		return err
	}

	ckpt.Status.NodeName = pod.Spec.NodeName
	ckpt.Status.PodSpecHash = util.ComputeHash(&pod.Spec)
	ckpt.Status.Phase = v1alpha1.CheckpointPending
	util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointPending), "InitializingCompleted", "pod spec hash has been configured")
	return nil
}

// pendingHandler is used for distributing grit agent pod to specified node which has the pod for checkpointing.
// checkpoint state will be upgraded to Checkpointing after grit agent pod becomes running.
func (c *Controller) pendingHandler(ctx context.Context, ckpt *v1alpha1.Checkpoint) error {
	// grit agent job is running, upgrade state to checkpointing when pod is ready
	var job batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: ckpt.Namespace, Name: ckpt.Name}, &job); err == nil {
		if job.Status.Ready != nil && *(job.Status.Ready) == 1 {
			ckpt.Status.Phase = v1alpha1.Checkpointing
			util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.Checkpointing), "GritAgentIsReady", fmt.Sprintf("grit agent pod(%s/%s) is ready", job.Namespace, job.Name))
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	gritAgentJob, err := c.agentManager.GenerateGritAgentJob(ctx, ckpt, nil)
	if err != nil {
		ckpt.Status.Phase = v1alpha1.CheckpointFailed
		util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "GenerateGritAgentFailed", fmt.Sprintf("failed to generate grit agent job, %v", err))
		return nil
	}

	// start to distribute grit agent job
	return c.Create(ctx, gritAgentJob)
}

// checkpointedHandler is used for garbage collecting grit agent pod. then pvc for cloud storage can be used for restoring.
// if checkpoint.Spec.AutoMigration is true, upgrade phase to checkpoint migrating.
func (c *Controller) checkpointedHandler(ctx context.Context, ckpt *v1alpha1.Checkpoint) error {
	var gritAgentJob batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: ckpt.Namespace, Name: ckpt.Name}, &gritAgentJob); client.IgnoreNotFound(err) != nil {
		return err
	} else if err == nil { // grit agent exist
		if gritAgentJob.DeletionTimestamp.IsZero() { // skip deleting grit agent job
			return c.Delete(ctx, &gritAgentJob)
		}
	} else { // grit agent job is deleted
		if ckpt.Spec.AutoMigration {
			ckpt.Status.Phase = v1alpha1.CheckpointMigrating
			util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointMigrating), "CheckpointedCompleted", "auto migrating is true and checkpoint task is completed.")
		}
	}

	return nil
}

// migratingHandler is used for creating Restore resource and deleting checkpointed pod.
func (c *Controller) migratingHandler(ctx context.Context, ckpt *v1alpha1.Checkpoint) error {
	// get checkpoint pod
	var checkpointPod corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: ckpt.Namespace, Name: ckpt.Spec.PodName}, &checkpointPod); err != nil {
		if apierrors.IsNotFound(err) {
			ckpt.Status.Phase = v1alpha1.CheckpointFailed
			util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "PodIsRemoved", fmt.Sprintf("migrating pod(%s) in checkpoint has been removed", ckpt.Spec.PodName))
			return nil
		} else {
			return err
		}
	}

	// resolve owner reference from checkpoint pod
	var ownerRef *metav1.OwnerReference
	for i := range checkpointPod.OwnerReferences {
		if *checkpointPod.OwnerReferences[i].Controller {
			ownerRef = &checkpointPod.OwnerReferences[i]
			break
		}
	}

	if ownerRef == nil {
		ckpt.Status.Phase = v1alpha1.CheckpointFailed
		util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "PodHasNoOwnerReference", fmt.Sprintf("migrating pod(%s) in checkpoint has no owner reference", ckpt.Spec.PodName))
		return nil
	}

	restore := v1alpha1.Restore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "auto-" + ckpt.Name,
			Namespace: ckpt.Namespace,
			Annotations: map[string]string{
				v1alpha1.PodSpecHashLabel: ckpt.Status.PodSpecHash,
			},
		},
		Spec: v1alpha1.RestoreSpec{
			CheckpointName: ckpt.Name,
			OwnerRef:       *ownerRef,
		},
	}

	if err := c.Create(ctx, &restore); client.IgnoreAlreadyExists(err) != nil {
		return err
	}

	// delete checkpoint pod
	if checkpointPod.DeletionTimestamp.IsZero() {
		if err := c.Delete(ctx, &checkpointPod); client.IgnoreNotFound(err) != nil {
			return err
		}
	}

	ckpt.Status.Phase = v1alpha1.CheckpointMigrated
	util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointMigrated), "AutoMigratingCompleted", "restore resource is created and checkpoint pod is removed.")
	return nil
}

// +kubebuilder:rbac:groups=kaito.sh,resources=checkpoints,verbs=list;watch;get
// +kubebuilder:rbac:groups=kaito.sh,resources=checkpoints/status,verbs=update
// +kubebuilder:rbac:groups=kaito.sh,resources=restores,verbs=create
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=list;watch;get;create;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;delete

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("checkpoint.lifecycle").
		For(&v1alpha1.Checkpoint{}).
		Watches(&batchv1.Job{}, &handler.EnqueueRequestForObject{}, builder.WithPredicates(util.GritAgentJobPredicate)).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Second, 300*time.Second),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
			MaxConcurrentReconciles: 5,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
