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

	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
)

const (
	GritAgentConfigMapName = "grit-agent-config"
	HostPathKey            = "host-path"
	GritAgentYamlKey       = "grit-agent.yaml"
	HostRootDirInContainer = "/mnt/host-data/"
	PvcRootDirInContainer  = "/mnt/pvc-data/"
)

type AgentManager struct {
	namespace string
	lister    corev1listers.ConfigMapLister
}

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

	if len(strings.TrimSpace(cm.Data[HostPathKey])) == 0 || len(cm.Data[GritAgentYamlKey]) == 0 {
		return nil, errors.New("There is no host-path or grit-agent.yaml in grit-agent-config")
	}

	girtAgentJobTemplate := cm.Data[GritAgentYamlKey]
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
				Path: strings.TrimSpace(cm.Data[HostPathKey]),
				Type: lo.ToPtr(corev1.HostPathDirectoryOrCreate),
			},
		},
	}
	gritAgentJob.Spec.Template.Spec.Volumes = append(gritAgentJob.Spec.Template.Spec.Volumes, pvcStorage, hostStorage)

	action := "ckpt"
	if restore != nil {
		action = "restore"
	}
	hostPath := filepath.Join(HostRootDirInContainer, action)
	pvcPath := filepath.Join(PvcRootDirInContainer, action)
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
