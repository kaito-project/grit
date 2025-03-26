// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package checkpoint

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"time"

	crmetadata "github.com/checkpoint-restore/checkpointctl/lib"
	runcoptions "github.com/containerd/containerd/api/types/runc/options"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/rootfs"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	internalapi "k8s.io/cri-api/pkg/apis"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	remote "k8s.io/cri-client/pkg"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kaito-project/grit/cmd/grit-agent/app/options"
	"github.com/kaito-project/grit/pkg/metadata"
)

func RuntimeCheckpointPod(ctx context.Context, opts *options.RuntimeCheckpointOptions) error {
	criClient, err := getRuntimeService(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to get runtime service: %w", err)
	}
	ctrClient, err := getContainerdClient(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to get containerd client: %w", err)
	}
	defer ctrClient.Close()

	// find containers
	containers, err := criClient.ListContainers(ctx, &runtimeapi.ContainerFilter{
		LabelSelector: map[string]string{
			"io.kubernetes.pod.name":      opts.TargetPodName,
			"io.kubernetes.pod.namespace": opts.TargetPodNamespace,
		},
		State: &runtimeapi.ContainerStateValue{
			State: runtimeapi.ContainerState_CONTAINER_RUNNING,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}
	if len(containers) == 0 {
		return fmt.Errorf("no containers found for pod %s/%s", opts.TargetPodNamespace, opts.TargetPodName)
	}

	// checkpoint each container
	// TODO: consider consistency problems when checkpointing multiple containers
	for _, container := range containers {
		if err := runtimeCheckpointContainer(ctx, container, ctrClient, opts); err != nil {
			return fmt.Errorf("failed to checkpoint container %s: %w", container.Id, err)
		}
	}

	return nil
}

func getRuntimeService(ctx context.Context, opts *options.RuntimeCheckpointOptions) (internalapi.RuntimeService, error) {
	logger := klog.Background()

	var tp trace.TracerProvider = noop.NewTracerProvider()
	timeout := time.Second * 10

	return remote.NewRemoteRuntimeService(opts.RuntimeEndpoint, timeout, tp, &logger)
}

func getContainerdClient(ctx context.Context, opts *options.RuntimeCheckpointOptions) (*containerd.Client, error) {
	ctrOpts := []containerd.Opt{
		containerd.WithTimeout(10 * time.Second),
	}

	return containerd.New(opts.RuntimeEndpoint, ctrOpts...)
}

func runtimeCheckpointContainer(ctx context.Context, ctrmeta *runtimeapi.Container, client *containerd.Client, opts *options.RuntimeCheckpointOptions) error {
	// checkpoint to a temporary directory, then perform a rename to ensure atomicity
	workPath := path.Join(opts.HostWorkPath, ctrmeta.GetMetadata().GetName()+"-work")
	logger := log.FromContext(ctx).WithValues("container", ctrmeta.Id, "workPath", workPath)
	ctx = log.IntoContext(ctx, logger)
	// ensure the work path exists
	if err := os.MkdirAll(workPath, 0755); err != nil {
		return fmt.Errorf("failed to create work path %s: %w", workPath, err)
	}

	logger.Info("Checkpointing container", "step", "pause container")
	ctx = namespaces.WithNamespace(ctx, "k8s.io")
	container, err := client.LoadContainer(ctx, ctrmeta.Id)
	if err != nil {
		return fmt.Errorf("failed to load container %s: %w", ctrmeta.Id, err)
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		return err
	}
	// pause if running
	if task != nil {
		if err := task.Pause(ctx); err != nil {
			return err
		}
		defer func() {
			if err := task.Resume(ctx); err != nil {
				log.FromContext(ctx).Error(err, "failed to resume task", "container", ctrmeta.Id)
			}
		}()
	}

	// dump criu image
	logger.Info("Checkpointing container", "step", "criu dump")
	checkpointPath := path.Join(workPath, crmetadata.CheckpointDirectory)
	if err := writeCriuCheckpoint(ctx, task, checkpointPath, workPath); err != nil {
		return fmt.Errorf("failed to write criu checkpoint: %w", err)
	}

	// dump rw layer
	logger.Info("Checkpointing container", "step", "write rootfs diff")
	rootFsDiffTarPath := path.Join(workPath, crmetadata.RootFsDiffTar)
	if err := writeRootFsDiffTar(ctx, ctrmeta, client, rootFsDiffTarPath); err != nil {
		return fmt.Errorf("failed to write rootfs diff tar: %w", err)
	}

	// save logs
	logger.Info("Checkpointing container", "step", "save container logs")
	containerLogPath := path.Join(getPodLogPath(opts), ctrmeta.GetMetadata().GetName())
	savePath := path.Join(workPath, metadata.ContainerLogFile)
	if err := writeContainerLog(ctx, containerLogPath, savePath); err != nil {
		// not a critical error, just log it
		logger.Info("Failed to save container log", "error", err)
	}

	// TODO: add config.dump and spec.dump

	// rename the work path to the final checkpoint path
	logger.Info("Checkpointing container", "step", "rename work path")
	checkpointDir := path.Join(opts.HostWorkPath, ctrmeta.GetMetadata().GetName())
	if err := os.Rename(workPath, checkpointDir); err != nil {
		return fmt.Errorf("failed to rename work path %s to checkpoint path %s: %w", workPath, checkpointDir, err)
	}

	logger.Info("Checkpointing container successfully")

	return nil
}

func withCheckpointOpts(imagePath, workPath string) containerd.CheckpointTaskOpts {
	return func(r *containerd.CheckpointTaskInfo) error {
		if r.Options == nil {
			r.Options = &runcoptions.CheckpointOptions{}
		}
		opts, _ := r.Options.(*runcoptions.CheckpointOptions)

		if imagePath != "" {
			opts.ImagePath = imagePath
		}
		if workPath != "" {
			opts.WorkPath = workPath
		}

		return nil
	}
}

func writeCriuCheckpoint(ctx context.Context, task containerd.Task, checkpointPath, criuWorkPath string) error {
	taskOpts := []containerd.CheckpointTaskOpts{
		withCheckpointOpts(checkpointPath, criuWorkPath),
	}
	_, err := task.Checkpoint(ctx, taskOpts...)
	if err != nil {
		return fmt.Errorf("failed to checkpoint task %s: %w", task.ID(), err)
	}
	return nil
}

func writeRootFsDiffTar(ctx context.Context, ctrmeta *runtimeapi.Container, client *containerd.Client, path string) error {
	c, err := client.ContainerService().Get(ctx, ctrmeta.Id)
	if err != nil {
		return fmt.Errorf("failed to get container %s: %w", ctrmeta.Id, err)
	}
	diffOpts := []diff.Opt{
		diff.WithReference(fmt.Sprintf("checkpoint-rw-%s", c.SnapshotKey)),
	}
	rw, err := rootfs.CreateDiff(ctx,
		c.SnapshotKey,
		client.SnapshotService(c.Snapshotter),
		client.DiffService(),
		diffOpts...,
	)
	if err != nil {
		return fmt.Errorf("failed to create diff for container %s: %w", ctrmeta.Id, err)
	}

	ra, err := client.ContentStore().ReaderAt(ctx, rw)
	if err != nil {
		return fmt.Errorf("failed to get reader for diff %v: %w", rw, err)
	}
	defer ra.Close()

	// the rw layer tarball
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", path, err)
	}
	defer f.Close()

	_, err = io.Copy(f, content.NewReader(ra))
	if err != nil {
		return fmt.Errorf("failed to copy diff to file %s: %w", path, err)
	}
	return nil
}

func getPodLogPath(opts *options.RuntimeCheckpointOptions) string {
	return path.Join(opts.KubeletLogPath, fmt.Sprintf("%s_%s_%s", opts.TargetPodNamespace, opts.TargetPodName, opts.TargetPodUID))
}

func writeContainerLog(ctx context.Context, logdir, savePath string) error {
	files, err := os.ReadDir(logdir)
	if err != nil {
		return fmt.Errorf("failed to read log directory %s: %w", logdir, err)
	}

	var logFiles []string
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		if path.Ext(file.Name()) == ".log" {
			logFiles = append(logFiles, file.Name())
		}
	}

	if len(logFiles) == 0 {
		log.FromContext(ctx).Info("No log files found, Skip")
		return nil
	}

	sort.Strings(logFiles)

	srcPath := path.Join(logdir, logFiles[len(logFiles)-1])
	log.FromContext(ctx).Info("Save log", "file", srcPath)
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open log file %s: %w", srcPath, err)
	}
	defer srcFile.Close()

	destFile, err := os.Create(savePath)
	if err != nil {
		return fmt.Errorf("failed to create destination file %s: %w", savePath, err)
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy log file to %s: %w", savePath, err)
	}

	return nil
}
