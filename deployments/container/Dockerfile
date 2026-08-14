# Copyright 2023 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Copyright (c) Advanced Micro Devices, Inc. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the \"License\");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an \"AS IS\" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

ARG GOLANG_VERSION=undefined
ARG BASE_IMAGE=registry.access.redhat.com/ubi9/ubi-minimal:9.6
FROM golang:${GOLANG_VERSION} as build

WORKDIR /build
COPY . .

ENV CGO_ENABLED=1
RUN mkdir /artifacts && \
    make PREFIX=/artifacts cmds

FROM ${BASE_IMAGE}

LABEL io.k8s.display-name="AMD GPU Driver for Dynamic Resource Allocation (DRA)"
LABEL name="AMD GPU Driver for Dynamic Resource Allocation (DRA)"
LABEL vendor="Advanced Micro Devices, Inc."
LABEL maintainer="Nitish Bhat <Nitish.Bhat@amd.com>"
ARG VERSION="latest"
LABEL version=${VERSION}
ARG RELEASE="latest"
LABEL release=${RELEASE}
LABEL summary="AMD DRA GPU driver for Kubernetes"
LABEL description="See summary"

COPY LICENSE LICENSE

RUN microdnf update -y && \
    microdnf install -y libdrm kmod && \
    microdnf clean all

# Copy the vendored AMD SMI library (third_party/amd_smi, sourced from the ROCm
# theRock tarball) for runtime linking, along with the rocm_sysdeps netlink
# libraries it DT_NEEDEDs.
COPY --from=build /build/third_party/amd_smi/lib/libamd_smi.so* /usr/lib64/
COPY --from=build /build/third_party/amd_smi/lib/librocm_sysdeps_*.so* /usr/lib64/

COPY --from=build /artifacts/gpu-kubeletplugin /usr/bin/gpu-kubeletplugin
COPY --from=build /artifacts/webhook           /usr/bin/webhook

ENV LD_LIBRARY_PATH=/usr/lib64
