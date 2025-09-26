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

MKDIR    ?= mkdir
TR       ?= tr
DIST_DIR ?= $(CURDIR)/dist
HELM     ?= "go run helm.sh/helm/v3/cmd/helm@latest"

# ---------------------------------------------------------------------------
# Environment generation (env.sh -> env.mk) single source of truth
# ---------------------------------------------------------------------------
ENV_SRC := $(CURDIR)/env.sh
ENV_MK  := env.mk


$(ENV_MK): $(ENV_SRC) hack/gen-env-mk.sh
	@bash hack/gen-env-mk.sh $(ENV_SRC) $(ENV_MK)

-include $(ENV_MK)

export IMAGE_GIT_TAG ?= $(shell git describe --tags --always --dirty --match 'v*')
export CHART_GIT_TAG ?= $(shell git describe --tags --always --dirty --match 'chart/*')

BUILDIMAGE_TAG ?= v1.0
BUILDIMAGE ?= $(DRIVER_IMAGE_REGISTRY)/$(DRIVER_IMAGE_NAME)-build:$(BUILDIMAGE_TAG)

CMDS := $(patsubst ./cmd/%/,%,$(sort $(dir $(wildcard ./cmd/*/))))
CMD_TARGETS := $(patsubst %,cmd-%, $(CMDS))

CHECK_TARGETS := assert-fmt vet lint ineffassign misspell
MAKE_TARGETS := build check vendor fmt test examples cmds coverage generate $(CHECK_TARGETS)

TARGETS := $(MAKE_TARGETS) $(CMD_TARGETS)

DOCKER_TARGETS := $(patsubst %,docker-%, $(TARGETS))
.PHONY: $(TARGETS) $(DOCKER_TARGETS)

GOOS ?= linux

ifneq ($(PREFIX),)
cmd-%: COMMAND_BUILD_OPTIONS = -o $(PREFIX)/$(*)
endif
cmds: $(CMD_TARGETS)
$(CMD_TARGETS): cmd-%:
	CGO_LDFLAGS_ALLOW='-Wl,--unresolved-symbols=ignore-in-object-files' GOOS=$(GOOS) \
		go build -ldflags "-s -w -X main.version=$(VERSION)" $(COMMAND_BUILD_OPTIONS) $(MODULE)/cmd/$(*)

build:
	@echo "Running repository build script: scripts/build-driver-image.sh"
	@bash scripts/build-driver-image.sh

all: build helm
check: $(CHECK_TARGETS)

# Update the vendor folder
vendor:
	go mod vendor

# Apply go fmt to the codebase
fmt:
	go list -f '{{.Dir}}' $(MODULE)/... \
		| xargs gofmt -s -l -w

assert-fmt:
	go list -f '{{.Dir}}' $(MODULE)/... \
		| xargs gofmt -s -l > fmt.out
	@if [ -s fmt.out ]; then \
		echo "\nERROR: The following files are not formatted:\n"; \
		cat fmt.out; \
		rm fmt.out; \
		exit 1; \
	else \
		rm fmt.out; \
	fi

ineffassign:
	ineffassign $(MODULE)/...

lint:
	golangci-lint run ./...

misspell:
	misspell $(MODULE)/...

vet:
	go vet $(MODULE)/...

COVERAGE_FILE := coverage.out
test:
	go test -v -coverprofile=$(COVERAGE_FILE) $(MODULE)/...

coverage: test
	cat $(COVERAGE_FILE) | grep -v "_mock.go" > $(COVERAGE_FILE).no-mocks
	go tool cover -func=$(COVERAGE_FILE).no-mocks

generate: generate-deepcopy

generate-deepcopy: vendor
	for api in $(APIS); do \
		rm -f $(CURDIR)/api/$(VENDOR)/resource/$${api}/zz_generated.deepcopy.go; \
		controller-gen \
			object:headerFile=$(CURDIR)/hack/boilerplate.generatego.txt \
			paths=$(CURDIR)/api/$(VENDOR)/resource/$${api}/ \
			output:object:dir=$(CURDIR)/api/$(VENDOR)/resource/$${api}; \
	done

setup-e2e:
	test/e2e/setup-e2e.sh

test-e2e:
	test/e2e/e2e.sh

teardown-e2e:
	test/e2e/teardown-e2e.sh

# Generate an image for containerized builds
# Note: This image is local only
.PHONY: .build-image
.build-image: docker/Dockerfile.build
	if [ x"$(SKIP_IMAGE_BUILD)" = x"" ]; then \
		docker build \
			--progress=plain \
			--build-arg GOLANG_VERSION="$(GOLANG_VERSION)" \
			--tag $(BUILDIMAGE) \
			-f $(^) \
			docker; \
	fi

$(DOCKER_TARGETS): docker-%: .build-image
	@echo "Running 'make $(*)' in container $(BUILDIMAGE)"
	docker run \
		--rm \
		-e HOME=$(PWD) \
		-e GOCACHE=$(PWD)/.cache/go \
		-e GOPATH=$(PWD)/.cache/gopath \
		-v $(PWD):$(PWD) --user $$(id -u):$$(id -g) \
		-w $(PWD) \
		$(BUILDIMAGE) \
			make $(*)

# Start an interactive shell using the development image.
.PHONY: .shell
.shell:
	docker run \
		--rm \
		-ti \
		-e HOME=$(PWD) \
		-e GOCACHE=$(PWD)/.cache/go \
		-e GOPATH=$(PWD)/.cache/gopath \
		$(CONTAINER_TOOL_OPTS) \
		-w $(PWD) \
		$(BUILDIMAGE)

.PHONY: push-release-artifacts
push-release-artifacts:
	CHART_VERSION="$${CHART_GIT_TAG##chart/}" HELM=$(HELM) scripts/build-driver-chart.sh >/dev/null
	export DRIVER_IMAGE_TAG="${IMAGE_GIT_TAG}"; \
	scripts/build-driver-image.sh && \
	scripts/push-driver-image.sh

.PHONY: push
# Build the image (using 'build' target) then push it using the repository push script.
push: build
	@echo "Pushing driver image using scripts/push-driver-image.sh"
	@bash scripts/push-driver-image.sh

DRIVER_NAME ?= k8s-gpu-dra-driver
CHART_DIR ?= $(CURDIR)/helm-charts-k8s

# Derive CHART_VERSION if not provided by reading Chart.yaml's version field
CHART_VERSION ?= $(shell sed -n 's/^version:[[:space:]]*//p' $(CHART_DIR)/Chart.yaml 2>/dev/null)

HELM_PACKAGE_NAME = $(DRIVER_NAME)-helm-k8s-$(CHART_VERSION).tgz
HELM_PACKAGE_PATH = $(CHART_DIR)/$(HELM_PACKAGE_NAME)

.PHONY: helm
helm: ## Package the Helm chart into helm-charts-k8s/$(HELM_PACKAGE_NAME)
	@if [ ! -d "$(CHART_DIR)" ]; then \
		echo "ERROR: Chart directory $(CHART_DIR) not found." >&2; \
		exit 1; \
	fi
	@pkg_file=$$( CHART_VERSION=$(CHART_VERSION) CHART_DIR=$(CHART_DIR) HELM=$(HELM) scripts/build-driver-chart.sh ); \
	if [ -z "$$pkg_file" ]; then \
		echo "ERROR: build-driver-chart.sh failed to produce a package" >&2; \
		exit 1; \
	fi; \
	mv "$$pkg_file" "$(HELM_PACKAGE_PATH)"; \
	echo "Created $(HELM_PACKAGE_PATH)"
