# Command-Line Options

This document describes the command-line flags accepted by the
gpu-kubeletplugin binary shipped with the AMD GPU DRA driver.

## gpu-kubeletplugin

The kubelet DRA plugin binary that discovers AMD GPUs and serves resource
allocation requests.

### Driver flags

| Flag | Type | Default | Env Var | Description |
|------|------|---------|---------|-------------|
| `--node-name` | string | *(required)* | `NODE_NAME` | The name of the Kubernetes node this instance runs on. Auto-populated via the downward API (`spec.nodeName`) in the Helm chart; does not need to be set manually. |
| `--cdi-root` | string | `/etc/cdi` | `CDI_ROOT` | Absolute path to the directory where CDI spec files will be generated. |
| `--kubelet-registrar-directory-path` | string | `/var/lib/kubelet/plugins_registry` | `KUBELET_REGISTRAR_DIRECTORY_PATH` | Absolute path to the directory where kubelet stores plugin registrations. |
| `--kubelet-plugins-directory-path` | string | `/var/lib/kubelet/plugins` | `KUBELET_PLUGINS_DIRECTORY_PATH` | Absolute path to the directory where kubelet stores plugin data. |
| `--healthcheck-port` | int | `-1` | `HEALTHCHECK_PORT` | Port for the gRPC healthcheck service. A positive value uses that port, `0` allocates a random port, and a negative value disables the service. |

### Kubernetes client flags

| Flag | Type | Default | Env Var | Description |
|------|------|---------|---------|-------------|
| `--kubeconfig` | string | *(empty — uses in-cluster config)* | `KUBECONFIG` | Absolute path to a kubeconfig file. Required when running the driver out of cluster. |
| `--kube-api-qps` | float | `5` | `KUBE_API_QPS` | QPS to use while communicating with the Kubernetes API server. |
| `--kube-api-burst` | int | `10` | `KUBE_API_BURST` | Burst to use while communicating with the Kubernetes API server. |

### Logging flags

| Flag | Type | Default | Env Var | Description |
|------|------|---------|---------|-------------|
| `-v` | int | `0` | `V` | Log level verbosity. Higher values produce more output. |
| `--logging-format` | string | `text` | `LOGGING_FORMAT` | Log output format. Permitted formats: `text`, `json`. |
| `--log-flush-frequency` | duration | `5s` | `LOG_FLUSH_FREQUENCY` | Maximum number of seconds between log flushes. |
| `--vmodule` | string | *(empty)* | `VMODULE` | Comma-separated list of `pattern=N` settings for file-filtered logging (text format only). |
| `--feature-gates` | string | `ContextualLogging=true` | `FEATURE_GATES` | A set of `key=value` pairs describing feature gates for alpha/experimental logging features. |
