# GRIT: GPU workload checkpointing and restoration

GRIT is a comprehensive solution designed to automate GPU workload migration within Kubernetes clusters. It enables users to capture the state of GPU workloads and restore them at a later time with minimal disruption.

Key features include:

- **Non-intrusive to Kubernetes core components**.
- **No application code changes required** - Applications can be checkpointed and restored without altering their source code.
- **Supports diverse workloads** – GRIT supports the migration of both pod-based workloads and standalone pods without necessitating changes to workload controller logic.
- **Efficient and secure checkpoint distribution** – Checkpoints are distributed using custom Persistent Volumes (PVs), offering better efficiency and security compared to OCI-based checkpoint images.
- **NVIDIA GPU workload support** – GRIT leverages [CRIU](https://github.com/checkpoint-restore/criu) and [cuda-checkpoint](https://github.com/NVIDIA/cuda-checkpoint) to enable checkpointing and restoration of NVIDIA GPU workloads.

# Architecture

![Architecture](docs/img/grit-arch.png)

The above diagram shows the architecture of GRIT. The main components are:
- **GRIT-Manager**: The control-plane component that orchestrates all checkpointing and restoration workflows. It includes controllers and admission webhooks required for lifecycle management.
- **GRIT-Agent**: It runs as Job Pod created by the GRIT-manager. It is responsible for transmitting checkpoint images and communication with GRIT-runtime.
- **GRIT-runtime**: A pluggable container runtime sitting between `kubelet` and `containerd`, receiving control plane signal from GRIT-Agent. It ultimately calls CRIU tools to checkpoint and restore the container process.

# Quick start

After installing GRIT, you can use the following commands to checkpoint and restore your workloads.

First, try to make a checkpoint of a workload.

Create a pv to store the checkpoint image:

```bash
$ cat examples/checkpoint-pvc.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ckpt-store
  namespace: default
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: azurefile-csi-premium
  resources:
    requests:
      storage: 256Gi

$ kubectl apply -f examples/checkpoint-pvc.yaml
```

Then start making checkpoints:

```bash
$ cat examples/checkpoint.yaml

apiVersion: kaito.sh/v1alpha1
kind: Checkpoint
metadata:
  name: demo
  namespace: default
spec:
  autoMigration: false
  podName: "falcon7b-tuning-cp4kz" # your pod name
  volumeClaim:
    claimName: "ckpt-store"

$ kubectl apply -f examples/checkpoint.yaml
```

If everything goes well, you will see the status of checkpoint CR is set to `Checkpointed`.

When you delete the original Pod, the newly created Pod will automatically be annotated with 'restore' information and will start by restoring from the checkpoint image.

# License

See [MIT LICENSE](LICENSE).

# Contact

"Kaito devs" <kaito-dev@microsoft.com>