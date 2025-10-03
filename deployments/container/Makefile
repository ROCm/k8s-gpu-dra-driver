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

##### Global variables #####
MKDIR ?= mkdir
DISTRIBUTIONS := ubi-minimal-9.6
DOCKERFILE := $(CURDIR)/deployments/container/Dockerfile

include $(CURDIR)/env.mk

DRIVER_IMAGE_TAG ?= $(VERSION)
IMAGE ?= $(DRIVER_IMAGE_REGISTRY)/$(DRIVER_IMAGE_NAME):$(DRIVER_IMAGE_TAG)

##### Public rules #####
.PHONY: $(DISTRIBUTIONS)

ubi-minimal-9.6: BASE_IMAGE = registry.access.redhat.com/ubi9/ubi-minimal:9.6

# Use a generic build target to build the relevant images
$(DISTRIBUTIONS):
	@echo "Building image: $(IMAGE) using base image: $(BASE_IMAGE)"
	DOCKER_BUILDKIT=1 \
		docker build --pull \
		--tag $(IMAGE) \
		--build-arg GOLANG_VERSION="$(GOLANG_VERSION)" \
		--build-arg BASE_IMAGE="$(BASE_IMAGE)" \
		--build-arg VERSION="$(VERSION)" \
		-f $(DOCKERFILE) \
		$(CURDIR)

push:
	@echo "Pushing image: $(IMAGE)"
	docker push $(IMAGE)
