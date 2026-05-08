# AMD GPU DRA Driver

Helm Chart Repository for the AMD GPU Dynamic Resource Allocation (DRA) driver for Kubernetes.

## Quick Start

```bash
# Install Helm
curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3
chmod 700 get_helm.sh
./get_helm.sh

# Add the Helm repository
helm repo add rocm-k8s-gpu-dra-driver https://rocm.github.io/k8s-gpu-dra-driver
helm repo update

# Install the DRA driver
helm install k8s-gpu-dra-driver rocm-k8s-gpu-dra-driver/k8s-gpu-dra-driver \
  --namespace kube-amd-gpu \
  --create-namespace
```

## Prerequisites

- Kubernetes 1.32 or newer (DRA entered beta in Kubernetes 1.32)
- AMD GPU hardware on the target nodes
- Helm 3.x

## More Information

For detailed documentation, configuration options, and troubleshooting, please refer to the [k8s-gpu-dra-driver GitHub repository](https://github.com/ROCm/k8s-gpu-dra-driver).
