// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package util

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/dump"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
)

const (
	ServerKey               = "server-key.pem"
	ServerCert              = "server-cert.pem"
	CACert                  = "ce-cert.pem"
	GritAgentJobNamePrefix  = "grit-agent-"
	KubeAPIAccessNamePrefix = "kube-api-access-"
)

type controllerNameKeyType struct{}
type webhookNameKeyType struct{}

var (
	controllerNameKey = controllerNameKeyType{}
	webhookNameKey    = webhookNameKeyType{}

	GritAgentJobPredicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			job, ok := e.Object.(*batchv1.Job)
			if !ok {
				return false
			}

			return IsGritAgentJob(job)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			job, ok := e.ObjectNew.(*batchv1.Job)
			if !ok {
				return false
			}

			return IsGritAgentJob(job)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			job, ok := e.Object.(*batchv1.Job)
			if !ok {
				return false
			}
			return IsGritAgentJob(job)
		},
	}

	RestorationPodPredicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			pod, ok := e.Object.(*corev1.Pod)
			if !ok {
				return false
			}

			return IsRestorationPod(pod)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			pod, ok := e.ObjectNew.(*corev1.Pod)
			if !ok {
				return false
			}

			return IsRestorationPod(pod)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
	}

	GritAgentJobHandler = handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		job, ok := obj.(*batchv1.Job)
		if !ok {
			return []reconcile.Request{}
		}

		if strings.HasPrefix(job.Name, GritAgentJobNamePrefix) {
			return []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{Namespace: job.Namespace, Name: GritAgentJobOwnerName(job)},
				},
			}
		}
		return []reconcile.Request{}
	})
)

func GritAgentJobName(ckpt *v1alpha1.Checkpoint, restore *v1alpha1.Restore) string {
	if ckpt != nil {
		return fmt.Sprintf("%s%s", GritAgentJobNamePrefix, ckpt.Name)
	} else if restore != nil {
		return fmt.Sprintf("%s%s", GritAgentJobNamePrefix, restore.Name)
	}
	return ""
}

func GritAgentJobOwnerName(job *batchv1.Job) string {
	if job != nil {
		if strings.HasPrefix(job.Name, GritAgentJobNamePrefix) {
			return strings.TrimPrefix(job.Name, GritAgentJobNamePrefix)
		}
	}
	return ""
}

func WithControllerName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, controllerNameKey, name)
}

func WithWebhookName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, webhookNameKey, name)
}

func ComputeHash(spec *corev1.PodSpec) string {
	// exclude fields which varied across nodes, like spec.NodeName, kube-api-access volume
	specCopy := spec.DeepCopy()
	specCopy.NodeName = ""
	for i := range specCopy.Volumes {
		if strings.HasPrefix(specCopy.Volumes[i].Name, KubeAPIAccessNamePrefix) {
			specCopy.Volumes[i].Name = ""
		}
	}

	for i := range specCopy.InitContainers {
		for j := range specCopy.InitContainers[i].VolumeMounts {
			if strings.HasPrefix(specCopy.InitContainers[i].VolumeMounts[j].Name, KubeAPIAccessNamePrefix) {
				specCopy.InitContainers[i].VolumeMounts[j].Name = ""
			}
		}
	}

	for i := range specCopy.Containers {
		for j := range specCopy.Containers[i].VolumeMounts {
			if strings.HasPrefix(specCopy.Containers[i].VolumeMounts[j].Name, KubeAPIAccessNamePrefix) {
				specCopy.Containers[i].VolumeMounts[j].Name = ""
			}
		}
	}

	hasher := fnv.New32a()
	hasher.Reset()
	fmt.Fprintf(hasher, "%v", dump.ForHash(specCopy))
	return fmt.Sprint(hasher.Sum32())
}

func IsGritAgentJob(job *batchv1.Job) bool {
	return job.Labels[v1alpha1.GritAgentLabel] == v1alpha1.GritAgentName
}

func IsRestorationPod(pod *corev1.Pod) bool {
	return len(pod.Annotations[v1alpha1.CheckpointDataPathLabel]) != 0
}

func UpdateCondition(clk clock.Clock, conditions *[]metav1.Condition, status metav1.ConditionStatus, conditionType, reason, message string) {
	if conditions == nil {
		return
	}

	if *conditions == nil {
		*conditions = []metav1.Condition{}
	}

	newCondition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.NewTime(clk.Now()),
	}

	for i, cond := range *conditions {
		if cond.Type == conditionType {
			// if the same condition exists, there is no need to update condition.
			if cond.Status == status &&
				cond.Reason == reason &&
				cond.Message == message {
				return
			}
			(*conditions)[i] = newCondition
			return
		}
	}

	*conditions = append(*conditions, newCondition)
}

func RemoveCondition(conditions *[]metav1.Condition, conditionType string) {
	for i, cond := range *conditions {
		if cond.Type == conditionType {
			(*conditions)[i] = (*conditions)[len(*conditions)-1]
			*conditions = (*conditions)[:len(*conditions)-1]
			return
		}
	}
}

// ResolveLastPhaseFromConditions is used for getting the last phase before failed, so state machine can move out of failed state if
// errors have been fixed.
func ResolveLastPhaseFromConditions(conditions []metav1.Condition, conditionOrders map[string]int, firstPhase string) string {
	phase := ""
	// if phase is RestoreFailed, we need to resolve conditions and find the last phase before failed.
	maxOrder := -1
	for _, cond := range conditions {
		if order, exists := conditionOrders[cond.Type]; exists && order > maxOrder {
			maxOrder = order
			phase = cond.Type
		}
	}

	// if there is no conditions, we should fall back to beginning.
	if phase == "" {
		phase = firstPhase
	}
	return phase
}
