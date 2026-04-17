---
name: Bug Report
about: Report a bug or unexpected behavior
title: ''
labels: bug
assignees: ''
---

## Environment

- **Kubernetes version:** (`kubectl version`)
- **Helm chart version:** (`helm list -n <namespace>`)
- **GPU model and count:** (`amd-smi list` or `lspci | grep AMD`)
- **Node OS and kernel version:** (`uname -a`)
- **AMD GPU kernel driver version:** (`modinfo amdgpu | grep ^version`)

## Describe the bug

<!-- What happened? What did you expect to happen instead? -->

## Steps to reproduce

1.
2.
3.

## Diagnostic output

Please attach or paste the output of the following commands:

- [ ] Driver pod logs: `kubectl logs -n <namespace> <pod> -c plugin`
- [ ] ResourceSlice listing: `kubectl get resourceslices -o yaml`
- [ ] ResourceClaim status (if applicable): `kubectl get resourceclaim <name> -o yaml`
- [ ] Pod describe output (if applicable): `kubectl describe pod <name>`

## Additional context

<!-- Any other information that might help (screenshots, config snippets, etc.) -->
