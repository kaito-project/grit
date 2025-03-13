// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"text/template"
	"time"

	"golang.org/x/time/rate"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers/util"
)

var (
	podPredicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			pod, ok := e.Object.(*corev1.Pod)
			if !ok {
				return false
			}

			if isGritAgentPodRunning(pod) {
				return true
			}
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			pod, ok := e.ObjectNew.(*corev1.Pod)
			if !ok {
				return false
			}

			if isGritAgentPodRunning(pod) {
				return true
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
	}
)

func isGritAgentPodRunning(pod *corev1.Pod) bool {
	if pod.Labels[v1alpha1.GritAgentLabel] == v1alpha1.GritAgentName && pod.Status.Phase == corev1.PodRunning {
		return true
	}

	return false
}

type CheckpointStateHandler func(ctx context.Context, ckpt *v1alpha1.Checkpoint) error

type Controller struct {
	client.Client
	clock            clock.Clock
	workingNamespace string
	statesMachine    map[v1alpha1.CheckpointPhase]CheckpointStateHandler
}

func NewController(clk clock.Clock, kubeClient client.Client, ns string) *Controller {
	c := &Controller{
		clock:            clk,
		Client:           kubeClient,
		workingNamespace: ns,
	}

	// v1alpha1.Checkpointing, v1alpha1.CheckpointFailed, v1alpha1.CheckpointMigrated,
	// these three states, girt-manager don't need to do anything.
	c.statesMachine = map[v1alpha1.CheckpointPhase]CheckpointStateHandler{
		v1alpha1.CheckpointInitializing: c.initializingHandler,
		v1alpha1.CheckpointPending:      c.pendingHandler,
		v1alpha1.Checkpointed:           c.checkpointedHandler,
		v1alpha1.CheckpointMigrating:    c.migratingHandler,
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
		c.removeCondition(updatedCkpt, string(v1alpha1.CheckpointFailed))
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
		phase = v1alpha1.CheckpointInitializing
		return phase
	} else if phase != v1alpha1.CheckpointFailed {
		return phase
	}

	// if phase is CheckpointFailed, we need to resolve conditions and find the last phase before failed.
	for _, cond := range ckpt.Status.Conditions {
		switch cond.Type {
		case string(v1alpha1.CheckpointPending):
			if phase != v1alpha1.Checkpointing && phase != v1alpha1.Checkpointed {
				phase = v1alpha1.CheckpointPending
			}
		case string(v1alpha1.Checkpointing):
			if phase != v1alpha1.Checkpointed {
				phase = v1alpha1.Checkpointing
			}
		case string(v1alpha1.Checkpointed):
			phase = v1alpha1.Checkpointed
		default:
			// skip
		}
	}

	// if there is no conditions, we should fall back to initlizing.
	if phase == v1alpha1.CheckpointFailed {
		phase = v1alpha1.CheckpointInitializing
	}
	return phase
}

// initializingHandler is used for initializing pod spec hash for checkpoint resource, then upgraded state to CheckpointPending.
func (c *Controller) initializingHandler(ctx context.Context, ckpt *v1alpha1.Checkpoint) error {
	var pod corev1.Pod
	if err := c.Get(ctx, client.ObjectKey{Namespace: ckpt.Namespace, Name: ckpt.Spec.PodName}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			ckpt.Status.Phase = v1alpha1.CheckpointFailed
			c.updateCondition(ckpt, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "PodNotExist", fmt.Sprintf("pod(%s) for checkpoint doesn't exist", ckpt.Spec.PodName))
			return nil
		}
		return err
	}

	ckpt.Status.NodeName = pod.Spec.NodeName
	ckpt.Status.PodSpecHash = util.ComputeHash(&pod.Spec)
	ckpt.Status.Phase = v1alpha1.CheckpointPending
	c.updateCondition(ckpt, metav1.ConditionTrue, string(v1alpha1.CheckpointPending), "InitializingCompleted", "pod spec hash has been configured")
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
			c.updateCondition(ckpt, metav1.ConditionTrue, string(v1alpha1.Checkpointing), "GritAgentIsReady", fmt.Sprintf("grit agent pod(%s/%s) is ready", job.Namespace, job.Name))
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	// grit agent job is not found, so go to create a grit agent job
	// get job template for girt agent component
	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Namespace: c.workingNamespace, Name: "grit-agent-config"}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			ckpt.Status.Phase = v1alpha1.CheckpointFailed
			c.updateCondition(ckpt, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "GritAgentConfigNotExist", fmt.Sprintf("ConfigMap(%s/grit-agent-config) for grit-agent doesn't exist", c.workingNamespace))
			return nil
		}
		return err
	}

	girtAgentJobTemplate := cm.Data["grit-agent.yaml"]
	templateCtx := map[string]string{
		"jobName":   ckpt.Name,
		"namespace": ckpt.Namespace,
		"nodeName":  ckpt.Status.NodeName,
	}
	gritAgentJob, err := convertToGritAgentJob(girtAgentJobTemplate, templateCtx)
	if err != nil {
		ckpt.Status.Phase = v1alpha1.CheckpointFailed
		c.updateCondition(ckpt, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "ConvertGritAgentFailed", fmt.Sprintf("failed to convert grit agent job, %v", err))
		return nil
	}

	// preare volumes and volume mount for job
	pvcStorage := corev1.Volume{
		Name: "pvc-data",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: ckpt.Spec.VolumeClaim,
		},
	}
	hostStorage := corev1.Volume{
		Name: "host-data",
		VolumeSource: corev1.VolumeSource{
			HostPath: ckpt.Spec.HostPath,
		},
	}
	gritAgentJob.Spec.Template.Spec.Volumes = append(gritAgentJob.Spec.Template.Spec.Volumes, pvcStorage, hostStorage)

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "host-data",
			MountPath: "/mnt/host-data/ckpt",
		},
		{
			Name:      "pvc-data",
			MountPath: "/mnt/pvc-data/ckpt",
		},
	}

	if len(gritAgentJob.Spec.Template.Spec.Containers) != 0 {
		gritAgentJob.Spec.Template.Spec.Containers[0].VolumeMounts = append(gritAgentJob.Spec.Template.Spec.Containers[0].VolumeMounts, volumeMounts...)
	}

	// start to distribute grit agent job
	return c.Create(ctx, gritAgentJob)
}

func convertToGritAgentJob(templateStr string, context interface{}) (*batchv1.Job, error) {
	resourceTemplate, err := template.New("grit").Option("missingkey=zero").Parse(templateStr)
	if err != nil {
		return nil, err
	}

	w := bytes.NewBuffer([]byte{})
	if err := resourceTemplate.Execute(w, context); err != nil {
		return nil, err
	}

	jobObj, _, err := scheme.Codecs.UniversalDeserializer().Decode(w.Bytes(), nil, nil)
	if err != nil {
		return nil, err
	}

	gritAgentJob, ok := jobObj.(*batchv1.Job)
	if !ok {
		return nil, errors.New("couldn't convert grit agent job")
	}

	return gritAgentJob, nil
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
			c.updateCondition(ckpt, metav1.ConditionTrue, string(v1alpha1.CheckpointMigrating), "CheckpointedCompleted", "auto migrating is true and checkpoint task is completed.")
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
			c.updateCondition(ckpt, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "PodIsRemoved", fmt.Sprintf("migrating pod(%s) in checkpoint has been removed", ckpt.Spec.PodName))
		} else {
			return err
		}
	}

	// create Restore resource
	labelSelector, err := c.GetOwnerLabelSelector(ctx, &checkpointPod)
	if err != nil {
		ckpt.Status.Phase = v1alpha1.CheckpointFailed
		c.updateCondition(ckpt, metav1.ConditionTrue, string(v1alpha1.CheckpointFailed), "OwnerLabelSelectorNotFound", err.Error())
		return nil
	}

	restore := v1alpha1.Restore{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ckpt.Name,
			Namespace: ckpt.Namespace,
		},
		Spec: v1alpha1.RestoreSpec{
			CheckpointName: ckpt.Name,
			Selector: &metav1.LabelSelector{
				MatchLabels: labelSelector,
			},
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
	c.updateCondition(ckpt, metav1.ConditionTrue, string(v1alpha1.CheckpointMigrated), "AutoMigratingCompleted", "restore resource is created and checkpoint pod is removed.")
	return nil
}

func ParseGroupVersion(apiVersion string) schema.GroupVersion {
	parts := strings.Split(apiVersion, "/")
	if len(parts) == 1 {
		return schema.GroupVersion{Group: "", Version: parts[0]}
	}

	return schema.GroupVersion{Group: parts[0], Version: parts[1]}
}

func (c *Controller) GetOwnerLabelSelector(ctx context.Context, pod *corev1.Pod) (map[string]string, error) {
	if len(pod.OwnerReferences) == 0 {
		return nil, fmt.Errorf("pod(%s/%s) has no OwnerReference", pod.Namespace, pod.Name)
	}

	ownerRef := pod.OwnerReferences[0]
	gv := ParseGroupVersion(ownerRef.APIVersion)
	gvk := schema.GroupVersionKind{Group: gv.Group, Version: gv.Version, Kind: ownerRef.Kind}

	switch gvk.Kind {
	case "Deployment":
		owner := &appsv1.Deployment{}
		if err := c.Get(ctx, client.ObjectKey{Name: ownerRef.Name, Namespace: pod.Namespace}, owner); err != nil {
			return nil, err
		}
		return owner.Spec.Selector.MatchLabels, nil

	case "ReplicaSet":
		owner := &appsv1.ReplicaSet{}
		if err := c.Get(ctx, client.ObjectKey{Name: ownerRef.Name, Namespace: pod.Namespace}, owner); err != nil {
			return nil, err
		}
		return owner.Spec.Selector.MatchLabels, nil

	case "StatefulSet":
		owner := &appsv1.StatefulSet{}
		if err := c.Get(ctx, client.ObjectKey{Name: ownerRef.Name, Namespace: pod.Namespace}, owner); err != nil {
			return nil, err
		}
		return owner.Spec.Selector.MatchLabels, nil

	case "DaemonSet":
		owner := &appsv1.DaemonSet{}
		if err := c.Get(ctx, client.ObjectKey{Name: ownerRef.Name, Namespace: pod.Namespace}, owner); err != nil {
			return nil, err
		}
		return owner.Spec.Selector.MatchLabels, nil

	case "Job":
		// Job has no spec.selector，so use metadata.labels instead
		owner := &batchv1.Job{}
		if err := c.Get(ctx, client.ObjectKey{Name: ownerRef.Name, Namespace: pod.Namespace}, owner); err != nil {
			return nil, err
		}
		return owner.Labels, nil

	default:
		return nil, fmt.Errorf("unsupported owner kind: %s", gvk.Kind)
	}
}

func (c *Controller) updateCondition(ckpt *v1alpha1.Checkpoint, status metav1.ConditionStatus, conditionType, reason, message string) {
	newCondition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.NewTime(c.clock.Now()),
	}

	for i, cond := range ckpt.Status.Conditions {
		if cond.Type == conditionType {
			// if the same condition exists, there is no need to update condition.
			if cond.Status == status &&
				cond.Reason == reason &&
				cond.Message == message {
				return
			}
			ckpt.Status.Conditions[i] = newCondition
			return
		}
	}

	ckpt.Status.Conditions = append(ckpt.Status.Conditions, newCondition)
}

func (c *Controller) removeCondition(ckpt *v1alpha1.Checkpoint, conditionType string) {
	idx := -1
	for i, cond := range ckpt.Status.Conditions {
		if cond.Type != conditionType {
			idx = i
			break
		}
	}

	if idx != -1 {
		ckpt.Status.Conditions[idx] = ckpt.Status.Conditions[len(ckpt.Status.Conditions)-1]
		ckpt.Status.Conditions = ckpt.Status.Conditions[:len(ckpt.Status.Conditions)-1]
	}
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("checkpoint.lifecycle").
		For(&v1alpha1.Checkpoint{}).
		Watches(&corev1.Pod{}, &handler.EnqueueRequestForObject{}, builder.WithPredicates(podPredicate)).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Second, 300*time.Second),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
			MaxConcurrentReconciles: 5,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
