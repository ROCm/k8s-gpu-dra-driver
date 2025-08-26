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
