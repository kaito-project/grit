// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package options

import (
	"os"

	"github.com/spf13/pflag"
)

type GritAgentOptions struct {
	Version         bool
	KubeClientQPS   int
	KubeClientBurst int
	Action          string
	SrcDir          string
	DstDir          string

	RuntimeCheckpointOptions
}

type RuntimeCheckpointOptions struct {
	TargetPodNamespace string
	TargetPodName      string
	TargetPodUID       string
	RuntimeEndpoint    string
	KubeletLogPath     string
	HostWorkPath       string
}

const (
	ActionCheckpoint = "checkpoint"
	ActionRestore    = "restore"
)

func NewGritAgentOptions() *GritAgentOptions {
	return &GritAgentOptions{
		Version:         false,
		KubeClientQPS:   50,
		KubeClientBurst: 100,
	}
}

func (o *GritAgentOptions) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&o.Version, "version", o.Version, "print the version information, and then exit")
	fs.IntVar(&o.KubeClientQPS, "kube-client-qps", o.KubeClientQPS, "the rate of qps to kube-apiserver.")
	fs.IntVar(&o.KubeClientBurst, "kube-client-burst", o.KubeClientBurst, "the max allowed burst of queries to the kube-apiserver.")
	fs.StringVar(&o.Action, "action", os.Getenv("ACTION"), "the action to be performed. Valid values are: 'checkpoint', 'restore'.")
	fs.StringVar(&o.SrcDir, "src-dir", o.SrcDir, "the source directory in agent container for C/R data.")
	fs.StringVar(&o.DstDir, "dst-dir", o.DstDir, "the destination directory in agent container for C/R data.")

	fs.StringVar(&o.TargetPodNamespace, "target-pod-namespace", os.Getenv("TARGET_NAMESPACE"), "the namespace of the target pod.")
	fs.StringVar(&o.TargetPodName, "target-pod-name", os.Getenv("TARGET_NAME"), "the name of the target pod.")
	fs.StringVar(&o.TargetPodUID, "target-pod-uid", os.Getenv("TARGET_UID"), "the UID of the target pod.")
	fs.StringVar(&o.RuntimeEndpoint, "runtime-endpoint", "/run/containerd/containerd.sock", "the endpoint of the container runtime.")
	fs.StringVar(&o.KubeletLogPath, "kubelet-log-path", "/var/log/pods", "the path of kubelet log.")
	fs.StringVar(&o.HostWorkPath, "host-work-path", o.HostWorkPath, "the work path on the host.")
}
