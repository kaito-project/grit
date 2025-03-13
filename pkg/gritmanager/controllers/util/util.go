// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package util

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/samber/lo"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/dump"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
)

const (
	hostRootDirInContainer = "/mnt/host-data/"
	pvcRootDirInContainer  = "/mnt/pvc-data/"
)

type controllerNameKeyType struct{}

var (
	controllerNameKey = controllerNameKeyType{}

	GritAgentJobPredicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			job, ok := e.Object.(*batchv1.Job)
			if !ok {
				return false
			}

			return GritAgentJobIsReady(job)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			job, ok := e.ObjectNew.(*batchv1.Job)
			if !ok {
				return false
			}

			return GritAgentJobIsReady(job)
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
)

func WithControllerName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, controllerNameKey, name)
}

func ComputeHash(spec *corev1.PodSpec) string {
	hasher := fnv.New32a()
	hasher.Reset()
	fmt.Fprintf(hasher, "%v", dump.ForHash(spec))
	return fmt.Sprint(hasher.Sum32())
}

func IsGritAgentJob(job *batchv1.Job) bool {
	return job.Labels[v1alpha1.GritAgentLabel] == v1alpha1.GritAgentName
}

func GritAgentJobIsReady(job *batchv1.Job) bool {
	if !IsGritAgentJob(job) {
		return false
	}

	if job.Status.Ready != nil && *(job.Status.Ready) == 1 {
		return true
	}

	return false
}

func IsRestorationPod(pod *corev1.Pod) bool {
	return len(pod.Labels[v1alpha1.CheckpointDataPathLabel]) != 0
}

func UpdateCondition(clk clock.Clock, conditions *[]metav1.Condition, status metav1.ConditionStatus, conditionType, reason, message string) {
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
		if cond.Type != conditionType {
			(*conditions)[i] = (*conditions)[len(*conditions)-1]
			*conditions = (*conditions)[:len(*conditions)-1]
			return
		}
	}
}

func GenerateGritAgentJob(ctx context.Context, kubeClient client.Client, ns string, ckpt *v1alpha1.Checkpoint, restore *v1alpha1.Restore) (*batchv1.Job, error) {
	var cm corev1.ConfigMap
	if err := kubeClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: "grit-agent-config"}, &cm); err != nil {
		return nil, err
	}

	if len(strings.TrimSpace(cm.Data["host-path"])) == 0 || len(cm.Data["grit-agent.yaml"]) == 0 {
		return nil, errors.New("There is no host-path or grit-agent.yaml in grit-agent-config")
	}

	girtAgentJobTemplate := cm.Data["grit-agent.yaml"]
	templateCtx := map[string]string{
		"namespace": ckpt.Namespace,
		"jobName":   ckpt.Name,
		"nodeName":  ckpt.Status.NodeName,
	}

	if restore != nil {
		templateCtx["jobName"] = restore.Name
		templateCtx["nodeName"] = restore.Status.NodeName
	}

	gritAgentJob, err := convertToGritAgentJob(girtAgentJobTemplate, templateCtx)
	if err != nil {
		return nil, err
	} else if len(gritAgentJob.Spec.Template.Spec.Containers) != 1 {
		return nil, errors.New("There should be only one container in grit-agent job")
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
			HostPath: &corev1.HostPathVolumeSource{
				Path: strings.TrimSpace(cm.Data["host-path"]),
				Type: lo.ToPtr(corev1.HostPathDirectoryOrCreate),
			},
		},
	}
	gritAgentJob.Spec.Template.Spec.Volumes = append(gritAgentJob.Spec.Template.Spec.Volumes, pvcStorage, hostStorage)

	action := "ckpt"
	if restore != nil {
		action = "restore"
	}
	hostPath := filepath.Join(hostRootDirInContainer, action)
	pvcPath := filepath.Join(pvcRootDirInContainer, action)
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "host-data",
			MountPath: hostPath,
		},
		{
			Name:      "pvc-data",
			MountPath: pvcPath,
		},
	}
	c := gritAgentJob.Spec.Template.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, volumeMounts...)

	// prepare command args, like src dir, dst dir, checkpoint, restore info.
	args := map[string]string{
		"src-dir":         hostPath,
		"dst-dir":         pvcPath,
		"namespace":       ckpt.Namespace,
		"checkpoint-name": ckpt.Name,
	}

	if restore != nil {
		args["src-dir"] = pvcPath
		args["dst-dir"] = hostPath
		args["restore-name"] = restore.Name
	}

	for k, v := range args {
		c.Args = append(c.Args, fmt.Sprintf("--%s=%s", k, v))
	}

	return gritAgentJob, nil
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
