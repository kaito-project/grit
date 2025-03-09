ctr -n k8s.io c checkpoint --task --rw --image-path /mnt/grit-runtime/ckpt/ tuning ckimg

ctr -n k8s.io c restore --live --rw --image-path /mnt/grit-runtime/ckpt/ tuning ckimg

ctr --debug -n k8s.io run --device nvidia.com/gpu=all --rm --mount type=bind,src=/mnt/grit-runtime/workspace/data,dst=/mnt/data,options=rbind:rw \
    --mount type=bind,src=/mnt/grit-runtime/workspace/config,dst=/mnt/config,options=rbind:rw \
    --mount type=bind,src=/mnt/grit-runtime/workspace/results,dst=/mnt/results,options=rbind:rw \
    aimodelsregistrytest.azurecr.io/falcon-7b:0.0.8 \
    tuning python3 /workspace/tfs/fine_tuning.py

sudo podman run --rm -d \
	--runtime=runc \
	--device nvidia.com/gpu=all \
    --env "CUDA_VISIBLE_DEVICES=0" \
    --security-opt=label=disable \
    -v /mnt/grit-runtime/workspace/data:/mnt/data:rw \
    -v /mnt/grit-runtime/workspace/config:/mnt/config:rw \
    -v /mnt/grit-runtime/workspace/results:/mnt/results:rw \
	--name cuda-counter \
        aimodelsregistrytest.azurecr.io/falcon-7b:0.0.8 \
        python3 /workspace/tfs/fine_tuning.py

podman container checkpoint -l -e ./pm-checkpoint.tar

podman container restore -i ./pm-checkpoint.tar
