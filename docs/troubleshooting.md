# Troubleshooting Guide

This guide helps cluster administrators diagnose common issues with the AMD GPU DRA driver and provides guidance for reporting problems.

## Quick diagnostic commands

Run these commands first when something isn't working:

```bash
# Check driver pod status
kubectl get pods -n <namespace> -l app.kubernetes.io/name=k8s-gpu-dra-driver

# Check driver pod logs
kubectl logs -n <namespace> <pod> -c plugin

# Check init container logs (if pod is stuck initializing)
kubectl logs -n <namespace> <pod> -c driver-init

# List ResourceSlices published by the driver
kubectl get resourceslices -o wide

# Inspect a specific ResourceSlice's attributes and capacities
kubectl get resourceslice <name> -o yaml

# Check ResourceClaim allocation status
kubectl get resourceclaim <name> -o yaml

# Check pod events for scheduling or claim errors
kubectl describe pod <name>
```

## Common issues

### Driver pod stuck in Init state

**Symptom:** Pod shows `Init:0/1` and the init container keeps restarting.

**Cause:** The `amdgpu` kernel module is not loaded on the node. The init container waits for `/sys/class/kfd` and `/sys/module/amdgpu/drivers/` to appear before allowing the driver to start.

**Resolution:**
1. Check if the kernel driver is loaded: `lsmod | grep amdgpu`
2. If missing, install the ROCm or `amdgpu-dkms` package for your distribution.
3. Verify the devices appear: `ls /sys/class/kfd/kfd/topology/nodes/`

### No ResourceSlices appearing

**Symptom:** `kubectl get resourceslices` returns no entries for a node where GPUs are expected.

**Cause:** The driver cannot discover GPUs via sysfs.

**Resolution:**
1. Check the driver pod logs for discovery errors: `kubectl logs -n <namespace> <pod> -c plugin`
2. Verify GPU hardware is visible on the node: `ls /sys/class/kfd/kfd/topology/nodes/`
3. Ensure the DaemonSet is scheduled to the correct nodes — check `nodeSelector` and `tolerations` in your Helm values.

### Expected attributes missing from ResourceSlices

**Symptom:** A CEL selector referencing an attribute fails, or an attribute you expect (based on documentation or examples) is absent from the ResourceSlice YAML.

**Cause:** The driver reads device attributes from sysfs at discovery time. If sysfs does not expose a particular attribute for your GPU model, it will not appear in the ResourceSlice. Documentation and examples may reference attributes that are not available on all hardware.

**Resolution:**
1. Inspect the ResourceSlice to see which attributes are actually published for your hardware:
   ```bash
   kubectl get resourceslice <name> -o yaml
   ```
2. Adjust your CEL selectors to reference only attributes that are present.
3. Check driver logs for warnings like `VRAM info not available` which indicate fallback values are being used.

### ResourceClaim stuck Pending

**Symptom:** A ResourceClaim stays in Pending state and the pod referencing it is also Pending.

**Cause:** No available device matches the claim's CEL selector, or no ResourceSlices exist.

**Resolution:**
1. Verify ResourceSlices are published (see diagnostic commands above).
2. Compare the claim's CEL selector against actual device attributes in the ResourceSlice.
3. Common mistakes:
   - Referencing attributes not available on the hardware (see "Expected attributes missing" above).
   - Using wrong attribute names or values.
   - Requesting more GPUs than are available on any single node.

### Pod stuck Pending after ResourceClaim is allocated

**Symptom:** The ResourceClaim shows as allocated, but the pod remains Pending or fails to start.

**Cause:** Possible issues with CDI spec generation, kubelet plugin communication, or device preparation.

**Resolution:**
1. Check the driver pod logs for errors during device preparation:
   ```bash
   kubectl logs -n <namespace> <pod> -c plugin | grep -i "prepare\|error"
   ```
2. Verify CDI specs are being written to the configured path (default: `/var/run/cdi`).
3. Check kubelet logs on the node for DRA-related errors.

### Partition-related issues

**Symptom:** Partition devices do not appear in ResourceSlices, or partition claims remain Pending.

**Cause:** GPUs must be pre-partitioned before the driver can discover them. The driver does not dynamically create or modify GPU partitions.

**Resolution:**
1. Verify GPUs are partitioned: `amd-smi partition --list`
2. Partition GPUs before deploying the driver. See the [AMD GPU Operator partitioning guide](https://instinct.docs.amd.com/projects/gpu-operator/en/latest/dcm/applying-partition-profiles.html).
3. After partitioning, restart the driver pods to trigger re-discovery.
4. See the partition examples in [docs/demo.md](demo.md) for the expected setup.

## Known limitations

- **No dynamic GPU partitioning:** GPUs must be pre-partitioned before driver deployment. The driver discovers existing partitions but does not create, modify, or remove them.
- **Kubernetes 1.32+ required:** The DRA APIs used by this driver require Kubernetes 1.32 or later. The specific API version (`v1`, `v1beta2`, `v1beta1`) varies by Kubernetes version — the Helm chart auto-detects this.
- **Sysfs-dependent attributes:** Device attributes are read from sysfs at discovery time. Attributes not exposed by the kernel driver or hardware will not appear in ResourceSlices. Documentation and examples may reference attributes that are not available on all GPU models.
- **VRAM fallback:** When sysfs does not report VRAM size, the driver uses a default fallback value and logs a warning. The reported memory capacity may not reflect actual hardware in this case.

## Reporting issues

When opening a GitHub issue, include the following information to help us diagnose the problem quickly.

### Environment

- Kubernetes version: `kubectl version`
- Helm chart version: `helm list -n <namespace>`
- GPU model and count: `amd-smi list` or `lspci | grep AMD`
- Node OS and kernel version: `uname -a`
- AMD GPU kernel driver version: `modinfo amdgpu | grep ^version`

### Diagnostic output

- Driver pod logs: `kubectl logs -n <namespace> <pod> -c plugin`
- ResourceSlice listing: `kubectl get resourceslices -o yaml`
- ResourceClaim status (if applicable): `kubectl get resourceclaim <name> -o yaml`
- Pod describe output (if applicable): `kubectl describe pod <name>`

### Description

- What you expected to happen
- What actually happened
- Steps to reproduce

Use the [bug report issue template](https://github.com/ROCm/k8s-gpu-dra-driver/issues/new?template=bug_report.md) for a structured format.
