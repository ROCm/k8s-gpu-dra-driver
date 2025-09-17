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

ARG GOLANG_VERSION=undefined
ARG BASE_IMAGE=registry.access.redhat.com/ubi9/ubi-minimal:9.6
FROM golang:${GOLANG_VERSION} as build

WORKDIR /build
COPY . .

RUN apt-get update && \
    apt-get install build-essential pkg-config libdrm-dev -y && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir /artifacts && \
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

RUN microdnf clean all && \
    microdnf update -y && \
    microdnf install -y gcc gcc-c++ make pkgconfig libdrm && \
    microdnf clean all

COPY --from=build /artifacts/gpu-kubeletplugin /usr/bin/gpu-kubeletplugin
COPY --from=build /artifacts/webhook           /usr/bin/webhook
