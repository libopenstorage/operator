STORAGE_OPERATOR_IMG=$(DOCKER_HUB_REPO)/$(DOCKER_HUB_STORAGE_OPERATOR_IMAGE):$(DOCKER_HUB_STORAGE_OPERATOR_TAG)
PX_DOC_HOST ?= https://docs.portworx.com
PX_INSTALLER_HOST ?= https://install.portworx.com

ifndef PKGS
PKGS := $(shell go list ./... 2>&1 | grep -v 'github.com/libopenstorage/operator/vendor' | grep -v 'pkg/client/informers/externalversions' | grep -v versioned | grep -v 'pkg/apis/core')
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

RELEASE_VER := 1.4.0
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
	dep ensure -v -update

vendor:
	dep ensure -v

lint:
	go get -u golang.org/x/lint/golint
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
	go get -v github.com/kisielk/errcheck
	errcheck -verbose -blank $(PKGS)

staticcheck:
	go get -u honnef.co/go/tools/cmd/staticcheck
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
		python:3 bash -c "pip3 install operator-courier==2.1.7 && \
			operator-courier --verbose push /deploy/olm-catalog/portworx $(QUAY_STORAGE_OPERATOR_REPO) $(QUAY_STORAGE_OPERATOR_APP) $(OLM_VERSION) \"$$QUAY_TOKEN\""

verify-catalog:
	docker run -it --rm \
		-v $(BASE_DIR)/deploy:/deploy \
		python:3 bash -c "pip3 install operator-courier==2.1.7 && operator-courier --verbose verify --ui_validate_io /deploy/olm-catalog/portworx"

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

mockgen:
	go get github.com/golang/mock/gomock
	go get github.com/golang/mock/mockgen
	mockgen -destination=pkg/mock/openstoragesdk.mock.go -package=mock github.com/libopenstorage/openstorage/api OpenStorageRoleServer,OpenStorageNodeServer,OpenStorageClusterServer
	mockgen -destination=pkg/mock/storagedriver.mock.go -package=mock github.com/libopenstorage/operator/drivers/storage Driver

clean: clean-release-manifest
	-rm -rf $(BIN)
	@echo "Deleting image "$(STORAGE_OPERATOR_IMG)
	-sudo docker rmi -f $(STORAGE_OPERATOR_IMG)
	go clean -i $(PKGS)
