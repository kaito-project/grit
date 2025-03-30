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
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
	"github.com/kaito-project/grit/pkg/gritmanager/agentmanager"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers/util"
)

var (
	checkpointConditionOrder = map[string]int{
		string(v1alpha1.CheckpointCreated):       1,
		string(v1alpha1.CheckpointPending):       2,
		string(v1alpha1.Checkpointing):           3,
		string(v1alpha1.Checkpointed):            4,
		string(v1alpha1.AutoMigrationSubmitting): 5,
		string(v1alpha1.AutoMigrationSubmitted):  6,
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

	// v1alpha1.CheckpointFailed, v1alpha1.AutoMigrationSubmitted,
	// these two states, girt-manager don't need to do anything.
	c.statesMachine = map[v1alpha1.CheckpointPhase]CheckpointStateHandler{
		v1alpha1.CheckpointCreated:       c.createdHandler,
		v1alpha1.CheckpointPending:       c.pendingHandler,
		v1alpha1.Checkpointing:           c.checkpointingHandler,
		v1alpha1.Checkpointed:            c.checkpointedHandler,
		v1alpha1.AutoMigrationSubmitting: c.submittingHandler,
	}

	return c
}

func (c *Controller) Reconcile(ctx context.Context, ckpt *v1alpha1.Checkpoint) (reconcile.Result, error) {
	ctx = util.WithControllerName(ctx, "checkpoint.lifecycle")

	updatedCkpt := ckpt.DeepCopy()
	phase := v1alpha1.CheckpointPhase(util.ResolveLastPhaseFromConditions(updatedCkpt.Status.Conditions, checkpointConditionOrder, string(v1alpha1.CheckpointCreated)))
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

// createdHandler is used for initializing pod spec hash for checkpoint resource, then upgraded state to CheckpointPending.
func (c *Controller) createdHandler(ctx context.Context, ckpt *v1alpha1.Checkpoint) error {
	if ckpt.Status.Phase == "" {
		ckpt.Status.Phase = v1alpha1.CheckpointCreated
		util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointCreated), "CheckpointIsCreated", "checkpoint resource is created")
		return nil
	}
	var pod corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: ckpt.Namespace, Name: ckpt.Spec.PodName}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			ckpt.Status.Phase = v1alpha1.CheckpointFailed
			util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "PodNotExist", fmt.Sprintf("pod(%s) for checkpoint doesn't exist", ckpt.Spec.PodName))
			return nil
		}
		return err
	}
	log.FromContext(ctx).Info("pod metadata", "metadata", pod.ObjectMeta, "checkpoint", ckpt.Name)

	ckpt.Status.NodeName = pod.Spec.NodeName
	ckpt.Status.PodSpecHash = util.ComputeHash(&pod.Spec)
	ckpt.Status.PodUID = string(pod.UID)
	ckpt.Status.Phase = v1alpha1.CheckpointPending
	util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointPending), "InitializingCompleted", "pod spec hash has been configured")
	return nil
}

// pendingHandler is used for distributing grit agent pod to specified node which has the pod for checkpointing.
// checkpoint state will be upgraded to Checkpointing after grit agent pod created.
func (c *Controller) pendingHandler(ctx context.Context, ckpt *v1alpha1.Checkpoint) error {
	// grit agent job is running, upgrade state to checkpointing when pod is ready
	var job batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: ckpt.Namespace, Name: util.GritAgentJobName(ckpt, nil)}, &job); err == nil {
		ckpt.Status.Phase = v1alpha1.Checkpointing
		util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.Checkpointing), "GritAgentIsCreated", fmt.Sprintf("grit agent job(%s/%s) for checkpoint is created", job.Namespace, job.Name))
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
	log.FromContext(ctx).Info("grit manager job", "object", *gritAgentJob)

	// start to distribute grit agent job
	return c.Create(ctx, gritAgentJob)
}

func (c *Controller) checkpointingHandler(ctx context.Context, ckpt *v1alpha1.Checkpoint) error {
	var gritAgentJob batchv1.Job
	var isCompleted, isFailed bool
	var err error
	if err = c.Get(ctx, client.ObjectKey{Namespace: ckpt.Namespace, Name: util.GritAgentJobName(ckpt, nil)}, &gritAgentJob); client.IgnoreNotFound(err) != nil {
		return err
	} else if err == nil {
		isCompleted, isFailed = jobCompletedOrFailed(&gritAgentJob)
		if isCompleted {
			var pvc corev1.PersistentVolumeClaim
			if err = c.Get(ctx, client.ObjectKey{Namespace: ckpt.Namespace, Name: ckpt.Spec.VolumeClaim.ClaimName}, &pvc); err != nil {
				return err
			}

			ckpt.Status.DataPath = fmt.Sprintf("%s://%s/%s", pvc.Spec.VolumeName, ckpt.Namespace, ckpt.Name)
			ckpt.Status.Phase = v1alpha1.Checkpointed
			util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.Checkpointed), "GritAgentJobCompleted", fmt.Sprintf("grit agent job(%s/%s) is completed", gritAgentJob.Namespace, gritAgentJob.Name))
			return nil
		}
	}

	// girt job is not found or failed
	if err != nil || isFailed {
		ckpt.Status.Phase = v1alpha1.CheckpointFailed
		util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "GritAgentJobFailed", fmt.Sprintf("failed to execute grit agent job(%s/%s) in checkpointing state", gritAgentJob.Namespace, gritAgentJob.Name))
	}
	return nil
}

func jobCompletedOrFailed(job *batchv1.Job) (bool, bool) {
	if job == nil {
		return false, false
	}

	if job.Status.Succeeded > 0 {
		return true, false
	}

	if job.Status.Failed > 0 {
		return false, true
	}

	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobComplete && cond.Status == "True" {
			return true, false
		}

		if cond.Type == batchv1.JobFailed && cond.Status == "True" {
			return false, true
		}
	}
	return false, false
}

// checkpointedHandler is used for garbage collecting grit agent pod. then pvc for cloud storage can be used for restoring.
// if checkpoint.Spec.AutoMigration is true, upgrade phase to checkpoint Submitting.
func (c *Controller) checkpointedHandler(ctx context.Context, ckpt *v1alpha1.Checkpoint) error {
	var gritAgentJob batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: ckpt.Namespace, Name: util.GritAgentJobName(ckpt, nil)}, &gritAgentJob); client.IgnoreNotFound(err) != nil {
		return err
	} else if err == nil { // grit agent exist
		if gritAgentJob.DeletionTimestamp.IsZero() { // skip deleting grit agent job
			deletePolicy := metav1.DeletePropagationForeground
			return c.Delete(ctx, &gritAgentJob, &client.DeleteOptions{PropagationPolicy: &deletePolicy})
		}
	} else { // grit agent job is deleted
		if ckpt.Spec.AutoMigration {
			ckpt.Status.Phase = v1alpha1.AutoMigrationSubmitting
			util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.AutoMigrationSubmitting), "CheckpointedCompleted", "auto migration is true and start to submit migration")
		}
	}

	return nil
}

// submittingHandler is used for submitting Restore resource and deleting checkpointed pod.
func (c *Controller) submittingHandler(ctx context.Context, ckpt *v1alpha1.Checkpoint) error {
	// get checkpoint pod
	var checkpointPod corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: ckpt.Namespace, Name: ckpt.Spec.PodName}, &checkpointPod); err != nil {
		if apierrors.IsNotFound(err) {
			ckpt.Status.Phase = v1alpha1.CheckpointFailed
			util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "PodIsRemoved", fmt.Sprintf("checkpointed pod(%s) referenced by checkpoint resource(%s) has been removed", ckpt.Spec.PodName, ckpt.Name))
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
		util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "PodHasNoOwnerReference", fmt.Sprintf("checkpointed pod(%s) referenced by checkpoint resource(%s) has no owner reference", ckpt.Spec.PodName, ckpt.Name))
		return nil
	}

	restore := v1alpha1.Restore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ckpt.Name,
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

	log.FromContext(ctx).Info("checkpoint pod spec", "name", checkpointPod.Name, "spec", checkpointPod.Spec)
	// delete checkpoint pod
	if checkpointPod.DeletionTimestamp.IsZero() {
		if err := c.Delete(ctx, &checkpointPod); client.IgnoreNotFound(err) != nil {
			return err
		}
	}

	ckpt.Status.Phase = v1alpha1.AutoMigrationSubmitted
	util.UpdateCondition(c.clock, &ckpt.Status.Conditions, metav1.ConditionTrue, string(v1alpha1.AutoMigrationSubmitted), "SubmittingCompleted", "restore resource is created and checkpoint pod is removed.")
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
		Watches(&batchv1.Job{}, util.GritAgentJobHandler, builder.WithPredicates(util.GritAgentJobPredicate)).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Second, 300*time.Second),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
			MaxConcurrentReconciles: 5,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
