# Demo and Examples Guide

This guide walks you through creating a local kind cluster, installing the AMD GPU DRA driver, and running example workloads to verify basic claims, sharing within a pod, sharing across pods, and partition-aware allocations.

## Prerequisites:

- Kubernetes 1.34+ cluster with DRA APIs (resource.k8s.io/v1) enabled
- kind v0.17.0+ (or an existing compatible cluster)
- kubectl v1.34+
- Helm v3+
- At least one node with AMD GPUs; partition-capable devices recommended
  - For the partition examples (sections D/E), GPUs must be pre-partitioned on the node(s). This driver does not dynamically partition GPUs yet.
    See: https://instinct.docs.amd.com/projects/gpu-operator/en/latest/dcm/applying-partition-profiles.html
- Docker installed (only if building/loading a local driver image)

See the [Installation & Developer Guide](https://github.com/ROCm/k8s-gpu-dra-driver/blob/main/docs/installation.md) for tools and cluster requirements for building the driver.

## 1. Create or reuse a kind cluster

Use the helper scripts to create and delete a local kind cluster.

```bash
./demo/create-cluster.sh
```
Note: This script will build the driver image locally, create the kind cluster and load the driver image into the cluster.

```bash
# When finished
./demo/delete-cluster.sh
```

If you built a driver image locally and need to load it into an existing cluster manually, you can run:

```bash
docker save -o driver_image.tar ${DRIVER_IMAGE}
kind load image-archive --name ${KIND_CLUSTER_NAME} driver_image.tar
rm driver_image.tar
```

## 2. Install the driver via Helm

You can install using a packaged chart or directly from the chart directory.

```bash
# From packaged chart (artifact created by `make helm`)
make helm
helm upgrade -i --create-namespace --namespace k8s-gpu-dra-driver \
  helm-charts-k8s/k8s-gpu-dra-driver-helm-k8s-<version>.tgz

# Or from chart directory with explicit image settings
helm install -i --create-namespace --namespace k8s-gpu-dra-driver \
  k8s-gpu-dra-driver helm-chart-k8s/ \
  --set image.repository=${DRIVER_IMAGE_REGISTRY}/${DRIVER_IMAGE_NAME} \
  --set image.tag=${DRIVER_IMAGE_TAG}
```

## 3. Verify driver installation

After installing the chart, confirm the driver published at least one `ResourceSlice` for the node(s) that have GPUs.

```bash
# List ResourceSlices across the cluster
kubectl get resourceslice -A

# Inspect a specific ResourceSlice (trimmed example)
kubectl get resourceslice <name> -o yaml
```

You should see a ResourceSlice with `apiVersion: resource.k8s.io/v1` and entries under `spec.devices` similar to the following (trimmed):

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata:
  name: k8s-gpu-dra-driver-cluster-worker-gpu.amd.com-xxxxx
spec:
  devices:
  - name: gpu-0-128
    attributes:
      cardIndex:
        int: 40
      deviceID:
        string: "994913940318892495"
      driverSrcVersion:
        string: 7AA72EC2C8F513519BE86FD
      driverVersion:
        version: 6.12.12
      family:
        string: AI
      partitionProfile:
        string: spx_nps1
      pciAddr:
        string: "0003:00:02.0"
      productName:
        string: AMD_Instinct_MI300X_OAM
      renderIndex:
        int: 168
      resource.kubernetes.io/pcieRoot:
        string: pci0003:00
      type:
        string: amdgpu
    capacity:
      computeUnits:
        value: "304"
      memory:
        value: 196592Mi
      simdUnits:
        value: "1216"
```

If no ResourceSlices appear, check the driver pod logs and the Helm release status. A common cause is the nodes lacking the OS device drivers/GPUs or the DRA driver failing to start due to missing host dependencies.

## 4. Run examples and verify

### A. Basic GPU resource claim

Apply the example and inspect the claim and pod:

```bash
kubectl apply -f example/example.yaml

kubectl get pods -n gpu-test
kubectl get resourceclaims -A -oyaml
```

Expect to see a single pod `pod1` Running, and the `ResourceClaim` with an allocation listing a device (for example `gpu-0-128`), with `reservedFor` referencing `pod1`.

```bash
  status:
    allocation:
      devices:
        results:
        - device: gpu-48-176
          driver: gpu.amd.com
          pool: k8s-gpu-dra-driver-cluster-worker
          request: gpu
      nodeSelector:
        nodeSelectorTerms:
        - matchFields:
          - key: metadata.name
            operator: In
            values:
            - k8s-gpu-dra-driver-cluster-worker
    reservedFor:
    - name: pod1
      resource: pods
      uid: fd805c5d-54dd-4119-a839-35c5f52e2290
```

Within the pod, verify with AMD SMI:

```bash
kubectl exec -it -n gpu-test pod1 -- amd-smi
```

Sample Output:
```bash
+------------------------------------------------------------------------------+
| AMD-SMI 26.0.0+37d158ab      amdgpu version: 6.12.12  ROCm version: 7.0.0    |
| Platform: Linux Guest (Passthr                                               |
|-------------------------------------+----------------------------------------|
| BDF                        GPU-Name | Mem-Uti   Temp   UEC       Power-Usage |
| GPU  HIP-ID  OAM-ID  Partition-Mode | GFX-Uti    Fan               Mem-Usage |
|=====================================+========================================|
| 0003:00:03.0    AMD Instinct MI300X | 0 %      41 °C   0           124/750 W |
|   0       0       0        SPX/NPS1 | 0 %        N/A           283/196592 MB |
+-------------------------------------+----------------------------------------+
+------------------------------------------------------------------------------+
| Processes:                                                                   |
|  GPU        PID  Process Name          GTT_MEM  VRAM_MEM  MEM_USAGE     CU % |
|==============================================================================|
|  No running processes found                                                  |
+------------------------------------------------------------------------------+
```

### B. GPU shared by multiple containers in the same pod

Clean up resources from any previous examples.

```bash
kubectl apply -f example/example-same-pod-multiple-containers-share.yaml

kubectl get pods -n gpu-test
kubectl get resourceclaims -A -oyaml

kubectl exec -it -n gpu-test pod1 -c ctr0 -- amd-smi list
kubectl exec -it -n gpu-test pod1 -c ctr1 -- amd-smi list
```

Expect 2/2 containers running in the same pod and the same device identity (UUID) reported across both containers.

Sample Output:

```bash
GPU: 0
    BDF: 0003:00:03.0
    UUID: b5ff74a1-0000-1000-806f-3d3e7b684465
    KFD_ID: 31896
    NODE_ID: 8
    PARTITION_ID: 0
```

### C. GPU shared by multiple pods via a pre-created claim

Clean up resources from any previous examples.

```bash
kubectl apply -f example/example-multiple-pod-share.yaml

kubectl get pods -n gpu-test
kubectl get resourceclaims -A -oyaml

kubectl exec -it -n gpu-test pod1 -- amd-smi list
kubectl exec -it -n gpu-test pod2 -- amd-smi list
```

Expect two pods Running and the same `ResourceClaim` referenced by both (`reservedFor` lists both pods), with consistent device identity.

Sample ResourceClaim Status:

```bash
  status:
    allocation:
      devices:
        results:
        - device: gpu-48-176
          driver: gpu.amd.com
          pool: k8s-gpu-dra-driver-cluster-worker
          request: gpu
      nodeSelector:
        nodeSelectorTerms:
        - matchFields:
          - key: metadata.name
            operator: In
            values:
            - k8s-gpu-dra-driver-cluster-worker
    reservedFor:
    - name: pod1
      resource: pods
      uid: 3df5c9a3-6a44-455c-8c19-c8f958968906
    - name: pod2
      resource: pods
      uid: 96cf1daa-7848-4afe-bb40-4fd25ae16870
```

### D. Partitions: two from the same parent GPU

> Note: These partition examples require that your GPUs are already partitioned on the host. The AMD GPU DRA driver does not currently perform dynamic partitioning.
> Refer to the AMD GPU Operator documentation for applying partition profiles:
> https://instinct.docs.amd.com/projects/gpu-operator/en/latest/dcm/applying-partition-profiles.html
> If partitions are not present, the claims will not match any devices and may remain Pending.

Clean up resources from any previous examples.

```bash
kubectl apply -f example/example-partitions-same-parent.yaml

kubectl get resourceclaims -n gpu-test two-partitions-same-parent -oyaml
```

Check the allocation results: both devices should be partitions (`type: amdgpu-partition`) and share the same `deviceID`.

### E. Partitions: two from distinct parent GPUs

> Note: As above, ensure GPUs are pre-partitioned before running this example. Dynamic partitioning is not supported by this driver yet.
> See partitioning guide: https://instinct.docs.amd.com/projects/gpu-operator/en/latest/dcm/applying-partition-profiles.html

```bash
kubectl apply -f example/example-partitions-distinct-parents.yaml

kubectl get resourceclaims -n gpu-test two-partitions-distinct-parents -oyaml
```

Check the allocation results: both devices are partitions with different `deviceID` values.

---

Troubleshooting tips:

- Ensure your cluster is Kubernetes 1.34 and DRA APIs are not disabled. The examples use `resource.k8s.io/v1`.
- If the example pods fail to pull images, pre-pull or change the image to one accessible in your environment.
- If using kind and a locally built image, ensure it is loaded into the cluster nodes (see step 1).
