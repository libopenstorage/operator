STORAGE_OPERATOR_IMG=$(DOCKER_HUB_REPO)/$(DOCKER_HUB_STORAGE_OPERATOR_IMAGE):$(DOCKER_HUB_STORAGE_OPERATOR_TAG)
PX_DOC_HOST ?= https://docs.portworx.com
PX_INSTALLER_HOST ?= https://install.portworx.com

export GO111MODULE=on
export GOFLAGS = -mod=vendor
HAS_GOMODULES := $(shell go help mod why 2> /dev/null)
ifndef HAS_GOMODULES
$(error operator can only be built with go 1.11+ which supports go modules)
endif

ifndef PKGS
# shell does not honor export command above, so we need to explicitly pass GOFLAGS here
PKGS := $(shell GOFLAGS=-mod=vendor go list ./... 2>&1 | grep -v 'pkg/client/informers/externalversions' | grep -v versioned | grep -v 'pkg/apis/core')
endif

GO_FILES := $(shell find . -name '*.go' | grep -v vendor | \
                                   grep -v '\.pb\.go' | \
                                   grep -v '\.pb\.gw\.go' | \
                                   grep -v 'externalversions' | \
                                   grep -v 'versioned' | \
                                   grep -v 'generated')

ifeq ($(BUILD_TYPE),debug)
BUILDFLAGS += -gcflags "-N -l"
endif

RELEASE_VER := 1.3.0
BASE_DIR    := $(shell git rev-parse --show-toplevel)
GIT_SHA     := $(shell git rev-parse --short HEAD)
BIN         := $(BASE_DIR)/bin

VERSION = $(RELEASE_VER)-$(GIT_SHA)
OLM_VERSION = $(RELEASE_VER)-$(BUILD_VER)-$(GIT_SHA)

LDFLAGS += "-s -w -X github.com/libopenstorage/operator/pkg/version.Version=$(VERSION)"
BUILD_OPTIONS := -ldflags=$(LDFLAGS)

.DEFAULT_GOAL=all
.PHONY: operator deploy clean vendor vendor-update test

all: operator pretest

vendor-update:
	go mod download

vendor-tidy:
	go mod tidy

vendor:
	go mod vendor

lint:
	(mkdir -p tools && cd tools && GO111MODULE=off && go get -v golang.org/x/lint/golint)
	for file in $(GO_FILES); do \
		golint $${file}; \
		if [ -n "$$(golint $${file})" ]; then \
			exit 1; \
		fi; \
	done

vet:
	go vet $(PKGS)

check-fmt:
	bash -c "diff -u <(echo -n) <(gofmt -l -d -s -e $(GO_FILES))"

errcheck:
	(mkdir -p tools && cd tools && GO111MODULE=off && go get -v github.com/kisielk/errcheck)
	errcheck -verbose -blank $(PKGS)

staticcheck:
	(mkdir -p tools && cd tools && GO111MODULE=off && go get -u honnef.co/go/tools/cmd/staticcheck)
	staticcheck $(PKGS)

pretest: check-fmt lint vet staticcheck

test:
	echo "" > coverage.txt
	for pkg in $(PKGS);	do \
		go test -v -coverprofile=profile.out -covermode=atomic -coverpkg=$${pkg}/... $${pkg} || exit 1; \
		if [ -f profile.out ]; then \
			cat profile.out >> coverage.txt; \
			rm profile.out; \
		fi; \
	done
	sed -i '/mode: atomic/d' coverage.txt
	sed -i '1d' coverage.txt
	sed -i '1s/^/mode: atomic\n/' coverage.txt

codegen:
	@echo "Generating CRD"
	@hack/update-codegen.sh

operator:
	@echo "Building the cluster operator binary"
	@cd cmd && CGO_ENABLED=0 go build $(BUILD_OPTIONS) -o $(BIN)/operator

container:
	@echo "Building container: docker build --tag $(STORAGE_OPERATOR_IMG) -f Dockerfile ."
	sudo docker build --tag $(STORAGE_OPERATOR_IMG) -f build/Dockerfile .

deploy:
	docker push $(STORAGE_OPERATOR_IMG)

deploy-catalog:
	@echo "Pushing operator catalog $(QUAY_STORAGE_OPERATOR_REPO)/$(QUAY_STORAGE_OPERATOR_APP):$(OLM_VERSION)"
	docker run -it --rm \
		-v $(BASE_DIR)/deploy:/deploy \
	  -e QUAY_TOKEN="$$QUAY_TOKEN" \
		python:3 bash -c "pip3 install operator-courier && \
			operator-courier --verbose push /deploy/olm-catalog/portworx $(QUAY_STORAGE_OPERATOR_REPO) $(QUAY_STORAGE_OPERATOR_APP) $(OLM_VERSION) \"$$QUAY_TOKEN\""

verify-catalog:
	docker run -it --rm \
		-v $(BASE_DIR)/deploy:/deploy \
		python:3 bash -c "pip3 install operator-courier && operator-courier --verbose verify --ui_validate_io /deploy/olm-catalog/portworx"

downloads: getconfigs get-release-manifest

cleanconfigs:
	rm -f "bin/configs/portworx-prometheus-rule.yaml"

getconfigs: cleanconfigs
	wget -q '$(PX_DOC_HOST)/samples/k8s/pxc/portworx-prometheus-rule.yaml' -P bin/configs

clean-release-manifest:
	rm -rf manifests

get-release-manifest: clean-release-manifest
	mkdir -p manifests
	wget -q '$(PX_INSTALLER_HOST)/versions' -O manifests/portworx-releases-local.yaml

clean: clean-release-manifest
	-rm -rf $(BIN)
	@echo "Deleting image "$(STORAGE_OPERATOR_IMG)
	-sudo docker rmi -f $(STORAGE_OPERATOR_IMG)
	go clean -i $(PKGS)
