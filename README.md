# AMD GPU Kubernetes Driver for Dynamic Resource Allocation (DRA)

This repository contains AMD GPU resource driver for use with the [Dynamic
Resource Allocation
(DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
feature of Kubernetes.

## Quickstart and Demo

### Prerequisites

* [GNU Make 3.81+](https://www.gnu.org/software/make/)
* [GNU Tar 1.34+](https://www.gnu.org/software/tar/)
* [docker v20.10+ (including buildx)](https://docs.docker.com/engine/install/) or [Podman v4.9+](https://podman.io/docs/installation)
* [kind v0.17.0+](https://kind.sigs.k8s.io/docs/user/quick-start/)
* [helm v3.7.0+](https://helm.sh/docs/intro/install/)
* [kubectl v1.18+](https://kubernetes.io/docs/reference/kubectl/)
* Enable Container Device Interface (CDI) on your container runtime:
  * `CRI-O`: CDI is enabled by default in CRI-O.
  * `containerd`: Modify the config file to enable the CDI, then restart the `containerd`. By default the config file path is `/etc/containerd/config.toml`.

```toml
[plugins]
  [plugins."io.containerd.grpc.v1.cri"]
    enable_cdi = true
    cdi_spec_dirs = ["/etc/cdi", "/var/run/cdi"]
```
```bash
sudo systemctl restart containerd
``` 
 
### Compile, Package and Deploy the DRA driver
* Build container image: 
  ```
  DRIVER_IMAGE_REGISTRY=docker.io DRIVER_IMAGE_NAME=yan1996/k8s-gpu-dra-driver DRIVER_IMAGE_TAG=latest ./demo/scripts/build-driver-image.sh
  ```
* Deploy the DRA driver with helm chart: Go to `deployments/helm/rocm-k8s-gpu-dra-driver` to check the helm chart, then you can package the chart and deploy it to your cluster. Make sure the DRA image is pointing to the image registry where you host your image

![Demo Apps Figure](demo/demo-apps.png?raw=true "Semantics of the applications requesting resources from the example DRA resource driver.")

## Demo example and Verification

## 1. Basic GPU resource claiming with DRA driver
   
  * `kubectl apply -f example/example.yaml`
  *  there is one pod running, the resource claim should show that the resource is getting allocated

  ```
  $ k get pods -A
  gpu-test       pod1                                         1/1     Running   0              111s
  $ kubectl get resourceclaims -A -oyaml
    spec:
    devices:
      requests:
      - exactly:
          allocationMode: ExactCount
          count: 1
          deviceClassName: gpu.amd.com
        name: gpu
  status:
    allocation:
      devices:
        results:
        - adminAccess: null
          device: gpu-0-128
          driver: gpu.amd.com
          pool: leto
          request: gpu
      nodeSelector:
        nodeSelectorTerms:
        - matchFields:
          - key: metadata.name
            operator: In
            values:
            - leto
    reservedFor:
    - name: pod1
      resource: pods
      uid: 5b828ae2-a4fd-4223-b29b-ce0d28135aa7
  ```
  * Within the pod's container the GPU device should be detected by the SMI library
  ```
  $ kubectl exec -it -n gpu-test       pod1 -- amd-smi list
  GPU: 0
      BDF: 0000:cc:00.0
      UUID: 14ff740f-0000-1000-808e-a028b658a5e5
      KFD_ID: 35824
      NODE_ID: 2
      PARTITION_ID: 0
  ```

## 2. GPU shared by containers within the same pod

  * `kubectl apply -f example/example-same-pod-multiple-containers-share.yaml`
  * there are 2 containers running within the same pod

  ```
  gpu-test       pod1                                         2/2     Running   0              4s
  ```
  * verify that those 2 containers are sharing the same GPU by checking GUID
  
  ```bash
  $ k get resourceclaim -A -oyaml
    reservedFor:
    - name: pod1
      resource: pods
      uid: 2e5dd9c1-f9f2-406a-a444-147141df95bd
  $ k exec -it -n gpu-test pod1 -c ctr0 -- rocm-smi


  ========================================= ROCm System Management Interface =========================================
  =================================================== Concise Info ===================================================
  Device  Node  IDs              Temp    Power  Partitions          SCLK    MCLK     Fan  Perf  PwrCap  VRAM%  GPU%
                (DID,     GUID)  (Edge)  (Avg)  (Mem, Compute, ID)
  ====================================================================================================================
  0       2     0x740f,   35824  41.0°C  44.0W  N/A, N/A, 0         800Mhz  1600Mhz  0%   auto  300.0W  0%     0%
  ====================================================================================================================
  =============================================== End of ROCm SMI Log ================================================
  $ k exec -it -n gpu-test pod1 -c ctr1 -- rocm-smi


  ========================================= ROCm System Management Interface =========================================
  =================================================== Concise Info ===================================================
  Device  Node  IDs              Temp    Power  Partitions          SCLK    MCLK     Fan  Perf  PwrCap  VRAM%  GPU%
                (DID,     GUID)  (Edge)  (Avg)  (Mem, Compute, ID)
  ====================================================================================================================
  0       2     0x740f,   35824  41.0°C  44.0W  N/A, N/A, 0         800Mhz  1600Mhz  0%   auto  300.0W  0%     0%
  ====================================================================================================================
  =============================================== End of ROCm SMI Log ================================================
  ```

## 3. GPU shared by multiple pods

  * `kubectl apply -f example/example-multiple-pod-share.yaml`
  * there are 2 pods running

  ```bash
  gpu-test       pod1                                         1/1     Running   0              6s
  gpu-test       pod2                                         1/1     Running   0              6s
  ```
  * verify that they are sharing the same GPU by checking GUID
  ```bash
  $ k get resourceclaim -A -oyaml
      reservedFor:
    - name: pod1
      resource: pods
      uid: 87371552-53d6-4287-bc7d-8a70dd91b706
    - name: pod2
      resource: pods
      uid: 4417805c-01a3-4443-94b1-dbf1a03f6d98
  $ kubectl exec -it -n gpu-test pod1 -- rocm-smi


  ========================================= ROCm System Management Interface =========================================
  =================================================== Concise Info ===================================================
  Device  Node  IDs              Temp    Power  Partitions          SCLK    MCLK     Fan  Perf  PwrCap  VRAM%  GPU%
                (DID,     GUID)  (Edge)  (Avg)  (Mem, Compute, ID)
  ====================================================================================================================
  0       2     0x740f,   35824  41.0°C  44.0W  N/A, N/A, 0         800Mhz  1600Mhz  0%   auto  300.0W  0%     0%
  ====================================================================================================================
  =============================================== End of ROCm SMI Log ================================================
  $ kubectl exec -it -n gpu-test pod2 -- rocm-smi


  ========================================= ROCm System Management Interface =========================================
  =================================================== Concise Info ===================================================
  Device  Node  IDs              Temp    Power  Partitions          SCLK    MCLK     Fan  Perf  PwrCap  VRAM%  GPU%
                (DID,     GUID)  (Edge)  (Avg)  (Mem, Compute, ID)
  ====================================================================================================================
  0       2     0x740f,   35824  41.0°C  44.0W  N/A, N/A, 0         800Mhz  1600Mhz  0%   auto  300.0W  0%     0%
  ====================================================================================================================
  =============================================== End of ROCm SMI Log ================================================
  ```


## Anatomy of a DRA resource driver

TBD

## Code Organization

TBD

## Best Practices

TBD

## References

For more information on the DRA Kubernetes feature and developing custom resource drivers, see the following resources:

* [Dynamic Resource Allocation in Kubernetes](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
* TBD

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).

You can reach the maintainers of this project at:

- [Slack](https://slack.k8s.io/)
- [Mailing List](https://groups.google.com/a/kubernetes.io/g/dev)

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).

[owners]: https://git.k8s.io/community/contributors/guide/owners.md
[Creative Commons 4.0]: https://git.k8s.io/website/LICENSE
