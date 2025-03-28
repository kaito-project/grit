// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package agentmanager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/samber/lo"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers/util"
)

const (
	GritAgentConfigMapName = "grit-agent-config"
	HostPathKey            = "host-path"
	GritAgentYamlKey       = "grit-agent-template.yaml"
	PvcDirInContainer      = "/mnt/pvc-data/"
)

type AgentManager struct {
	namespace string
	lister    corev1listers.ConfigMapLister
}

// +kubebuilder:rbac:groups="",resources=configmaps,verbs=list;watch;get

func NewAgentManager(ns string, lister corev1listers.ConfigMapLister) *AgentManager {
	return &AgentManager{
		namespace: ns,
		lister:    lister,
	}
}

func (m *AgentManager) GetHostPath() string {
	cm, err := m.lister.ConfigMaps(m.namespace).Get(GritAgentConfigMapName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cm.Data[HostPathKey])
}

func (m *AgentManager) GenerateGritAgentJob(ctx context.Context, ckpt *v1alpha1.Checkpoint, restore *v1alpha1.Restore) (*batchv1.Job, error) {
	cm, err := m.lister.ConfigMaps(m.namespace).Get(GritAgentConfigMapName)
	if err != nil {
		return nil, err
	}

	if cm.Data == nil || len(strings.TrimSpace(cm.Data[HostPathKey])) == 0 || len(cm.Data[GritAgentYamlKey]) == 0 {
		return nil, errors.New("There is no host-path or grit-agent-template.yaml in grit-agent-config")
	}

	girtAgentJobTemplate := cm.Data[GritAgentYamlKey]
	templateCtx := map[string]string{
		"namespace": ckpt.Namespace,
		"jobName":   util.GritAgentJobName(ckpt, nil),
		"nodeName":  ckpt.Status.NodeName,
	}

	if restore != nil {
		templateCtx["jobName"] = util.GritAgentJobName(nil, restore)
		templateCtx["nodeName"] = restore.Status.NodeName
	}

	gritAgentJob, err := convertToGritAgentJob(girtAgentJobTemplate, templateCtx)
	if err != nil {
		return nil, err
	} else if len(gritAgentJob.Spec.Template.Spec.Containers) != 1 {
		return nil, errors.New("There should be only one container in grit-agent job")
	}
	log.FromContext(ctx).Info("grit manager job template", "object", *gritAgentJob)

	// preare volumes and volume mount for job
	pvcStorage := corev1.Volume{
		Name: "pvc-data",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: ckpt.Spec.VolumeClaim,
		},
	}

	hostPath := filepath.Join(strings.TrimSpace(cm.Data[HostPathKey]), ckpt.Namespace, ckpt.Name)
	hostStorage := corev1.Volume{
		Name: "host-data",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: hostPath,
				Type: lo.ToPtr(corev1.HostPathDirectoryOrCreate),
			},
		},
	}
	gritAgentJob.Spec.Template.Spec.Volumes = append(gritAgentJob.Spec.Template.Spec.Volumes, pvcStorage, hostStorage)

	pvcDataPath := filepath.Join(PvcDirInContainer, ckpt.Namespace, ckpt.Name)
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "host-data",
			MountPath: hostPath,
		},
		{
			Name:      "pvc-data",
			MountPath: PvcDirInContainer,
		},
	}
	c := &gritAgentJob.Spec.Template.Spec.Containers[0]
	c.VolumeMounts = append(c.VolumeMounts, volumeMounts...)

	action := "checkpoint"
	if restore != nil {
		action = "restore"
	}
	// prepare command args, like src-dir, dst-dir,etc.
	args := map[string]string{
		"action":         action,
		"src-dir":        hostPath,
		"dst-dir":        pvcDataPath,
		"host-work-path": hostPath,
	}

	if restore != nil {
		args["src-dir"] = pvcDataPath
		args["dst-dir"] = hostPath
	}

	for k, v := range args {
		c.Args = append(c.Args, fmt.Sprintf("--%s=%s", k, v))
	}

	c.Env = append(c.Env,
		corev1.EnvVar{Name: "TARGET_NAMESPACE", Value: ckpt.Namespace},
		corev1.EnvVar{Name: "TARGET_NAME", Value: ckpt.Spec.PodName},
		corev1.EnvVar{Name: "TARGET_UID", Value: ckpt.Status.PodUID},
	)
	return gritAgentJob, nil
}

func convertToGritAgentJob(templateStr string, context map[string]string) (*batchv1.Job, error) {
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
	} else if jobObj == nil {
		return nil, errors.New("failed to decode grit agent job object")
	}

	gritAgentJob, ok := jobObj.(*batchv1.Job)
	if !ok {
		return nil, errors.New("couldn't convert grit agent job")
	}

	return gritAgentJob, nil
}
