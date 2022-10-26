# set defaults
ifndef DOCKER_HUB_REPO
    DOCKER_HUB_REPO := portworx
    $(warning DOCKER_HUB_REPO not defined, using '$(DOCKER_HUB_REPO)' instead)
endif
ifndef DOCKER_HUB_OPERATOR_IMG
    DOCKER_HUB_OPERATOR_IMG := px-operator
    $(warning DOCKER_HUB_OPERATOR_IMG not defined, using '$(DOCKER_HUB_OPERATOR_IMG)' instead)
endif
ifndef DOCKER_HUB_OPERATOR_TAG
    DOCKER_HUB_OPERATOR_TAG := latest
    $(warning DOCKER_HUB_OPERATOR_TAG not defined, using '$(DOCKER_HUB_OPERATOR_TAG)' instead)
endif
ifndef DOCKER_HUB_OPERATOR_TEST_IMG
    DOCKER_HUB_OPERATOR_TEST_IMG := px-operator-test
    $(warning DOCKER_HUB_OPERATOR_TEST_IMG not defined, using '$(DOCKER_HUB_OPERATOR_TEST_IMG)' instead)
endif
ifndef DOCKER_HUB_OPERATOR_TEST_TAG
    DOCKER_HUB_OPERATOR_TEST_TAG := latest
    $(warning DOCKER_HUB_OPERATOR_TEST_TAG not defined, using '$(DOCKER_HUB_OPERATOR_TEST_TAG)' instead)
endif
ifndef DOCKER_HUB_BUNDLE_IMG
    DOCKER_HUB_BUNDLE_IMG := portworx-certified-bundle
    $(warning DOCKER_HUB_BUNDLE_IMG not defined, using '$(DOCKER_HUB_BUNDLE_IMG)' instead)
endif
ifndef DOCKER_HUB_REGISTRY_IMG
    DOCKER_HUB_REGISTRY_IMG := px-operator-registry
    $(warning DOCKER_HUB_REGISTRY_IMG not defined, using '$(DOCKER_HUB_REGISTRY_IMG)' instead)
endif
ifndef BASE_REGISTRY_IMG
    BASE_REGISTRY_IMG := docker.io/portworx/px-operator-registry:1.10.0
    $(warning BASE_REGISTRY_IMG not defined, using '$(BASE_REGISTRY_IMG)' instead)
endif

HAS_GOMODULES := $(shell go help mod why 2> /dev/null)

ifdef HAS_GOMODULES
export GO111MODULE=on
export GOFLAGS=-mod=vendor
else
$(error operator can only be built with go 1.11+ which supports go modules)
endif

ifndef PKGS
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

RELEASE_VER := 1.10.0
BASE_DIR    := $(shell git rev-parse --show-toplevel)
GIT_SHA     := $(shell git rev-parse --short HEAD)
BIN         := $(BASE_DIR)/bin

VERSION = $(RELEASE_VER)-$(GIT_SHA)

OPERATOR_IMG=$(DOCKER_HUB_REPO)/$(DOCKER_HUB_OPERATOR_IMG):$(DOCKER_HUB_OPERATOR_TAG)
OPERATOR_TEST_IMG=$(DOCKER_HUB_REPO)/$(DOCKER_HUB_OPERATOR_TEST_IMG):$(DOCKER_HUB_OPERATOR_TEST_TAG)
BUNDLE_IMG=$(DOCKER_HUB_REPO)/$(DOCKER_HUB_BUNDLE_IMG):$(RELEASE_VER)
REGISTRY_IMG=$(DOCKER_HUB_REPO)/$(DOCKER_HUB_REGISTRY_IMG):$(RELEASE_VER)
PX_DOC_HOST ?= https://docs.portworx.com
PX_INSTALLER_HOST ?= https://install.portworx.com
PROMETHEUS_OPERATOR_HELM_CHARTS_TAG ?= kube-prometheus-stack-19.0.1
PROMETHEUS_OPERATOR_CRD_URL_PREFIX ?= https://raw.githubusercontent.com/prometheus-community/helm-charts/$(PROMETHEUS_OPERATOR_HELM_CHARTS_TAG)/charts/kube-prometheus-stack/crds
CSI_SNAPSHOTTER_V3_CRD_URL_PREFIX ?= https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v3.0.3/client/config/crd
CSI_SNAPSHOTTER_V4_CRD_URL_PREFIX ?= https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/v4.2.1/client/config/crd

BUNDLE_DIR         := $(BASE_DIR)/deploy/olm-catalog/portworx
RELEASE_BUNDLE_DIR := $(BUNDLE_DIR)/$(RELEASE_VER)
BUNDLE_VERSIONS    := $(shell find $(BUNDLE_DIR) -mindepth 1 -maxdepth 1 -type d )

LDFLAGS += "-s -w -X github.com/libopenstorage/operator/pkg/version.Version=$(VERSION)"
BUILD_OPTIONS := -ldflags=$(LDFLAGS)

.DEFAULT_GOAL=all
.PHONY: operator deploy clean vendor vendor-update test

all: operator pretest downloads

vendor-update:
	go mod download

vendor:
	go mod vendor

# Tools download  (if missing)
# - please make sure $GOPATH/bin is in your path, also do not use $GOBIN

$(GOPATH)/bin/golint:
	go get -u golang.org/x/lint/golint

$(GOPATH)/bin/errcheck:
	GO111MODULE=off go get -u github.com/kisielk/errcheck

$(GOPATH)/bin/staticcheck:
	GOFLAGS="" go install honnef.co/go/tools/cmd/staticcheck@v0.2.1

$(GOPATH)/bin/revive:
	GO111MODULE=off go get -u github.com/mgechev/revive

$(GOPATH)/bin/gomock:
	go get -u github.com/golang/mock/gomock

$(GOPATH)/bin/mockgen:
	go get -u github.com/golang/mock/mockgen

# Static checks

vendor-tidy:
	go mod tidy

lint: $(GOPATH)/bin/golint
	# golint check ...
	@for file in $(GO_FILES); do \
		golint $${file}; \
		if [ -n "$$(golint $${file})" ]; then \
			exit 1; \
		fi; \
	done

vet:
	# go vet check ...
	@go vet $(PKGS)

check-fmt:
	# gofmt check ...
	@bash -c "diff -u <(echo -n) <(gofmt -l -d -s -e $(GO_FILES))"

errcheck: $(GOPATH)/bin/errcheck
	# errcheck check ...
	@errcheck -verbose -blank $(PKGS)

staticcheck: $(GOPATH)/bin/staticcheck
	# staticcheck check ...
	@staticcheck $(PKGS)

revive: $(GOPATH)/bin/revive
	# revive check ...
	@revive -formatter friendly $(PKGS)

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

integration-test:
	@echo "Building operator integration tests"
	@cd test/integration_test && go test -tags integrationtest,fafb -v -c -o operator.test

integration-test-container:
	@echo "Building operator test container $(OPERATOR_TEST_IMG)"
	@cd test/integration_test && docker build --tag $(OPERATOR_TEST_IMG) -f Dockerfile .

integration-test-deploy:
	@echo "Pushing operator test container $(OPERATOR_TEST_IMG)"
	docker push $(OPERATOR_TEST_IMG)

codegen:
	@echo "Generating CRD"
	(GOFLAGS="" hack/update-codegen.sh)

operator:
	@echo "Building the cluster operator binary"
	@cd cmd/operator && CGO_ENABLED=0 go build $(BUILD_OPTIONS) -o $(BIN)/operator
	@cd cmd/dryrun && CGO_ENABLED=0 go build $(BUILD_OPTIONS) -o $(BIN)/dryrun

container:
	@echo "Building operator image $(OPERATOR_IMG)"
	docker build --pull --tag $(OPERATOR_IMG) -f build/Dockerfile .

deploy:
	@echo "Pushing operator image $(OPERATOR_IMG)"
	docker push $(OPERATOR_IMG)

verify-bundle-dir:
	docker run -it --rm \
		-v $(BASE_DIR)/deploy:/deploy \
		python:3 bash -c "pip3 install operator-courier==2.1.11 && operator-courier --verbose verify --ui_validate_io /deploy/olm-catalog/portworx"

bundle: clean-bundle build-bundle deploy-bundle #validate-bundle

build-bundle:
	@rm -rf $(RELEASE_BUNDLE_DIR)/manifests $(RELEASE_BUNDLE_DIR)/metadata $(RELEASE_BUNDLE_DIR)/bundle.Dockerfile
	@rm -rf $(RELEASE_BUNDLE_DIR)/bundle_* $(RELEASE_BUNDLE_DIR)/bundle.Dockerfile
	@echo "Building operator bundle image $(BUNDLE_IMG)"
	opm alpha bundle build \
		-d $(RELEASE_BUNDLE_DIR) -u $(RELEASE_BUNDLE_DIR) \
		-b docker -t $(BUNDLE_IMG)

deploy-bundle:
	@echo "Pushing operator bundle image $(BUNDLE_IMG)"
	docker push $(BUNDLE_IMG)

validate-bundle:
	opm alpha bundle validate -b docker -t $(BUNDLE_IMG)

clean-bundle:
	@rm -f bundle.Dockerfile
	@for version_dir in $(BUNDLE_VERSIONS); do \
		echo "Cleaning bundle directory $${version_dir}"; \
		rm -rf $${version_dir}/manifests; \
		rm -rf $${version_dir}/metadata; \
		rm -rf $${version_dir}/bundle_*; \
		rm -rf $${version_dir}/bundle.Dockerfile; \
	done

catalog: build-catalog deploy-catalog

build-catalog:
	@echo "Building operator registry image $(REGISTRY_IMG)"
	opm index add -u docker -p docker \
		--bundles docker.io/$(BUNDLE_IMG) \
		--from-index $(BASE_REGISTRY_IMG) \
		--tag $(REGISTRY_IMG)

deploy-catalog:
	@echo "Pushing operator registry image $(REGISTRY_IMG)"
	docker push $(REGISTRY_IMG)

downloads: getconfigs get-release-manifest

cleanconfigs:
	rm -rf bin/configs

# TODO: import ccm-go repo as a git submodule: https://github.dev.purestorage.com/parts/ccm-go
getccmconfigs:
	mkdir -p bin/configs
	cp deploy/ccm/* bin/configs/

getconfigs: cleanconfigs getccmconfigs
	wget -q '$(PX_DOC_HOST)/samples/k8s/pxc/portworx-prometheus-rule.yaml' -P bin/configs --no-check-certificate
	wget -q '$(PROMETHEUS_OPERATOR_CRD_URL_PREFIX)/crd-alertmanagerconfigs.yaml' -O bin/configs/prometheus-crd-alertmanagerconfigs.yaml
	wget -q '$(PROMETHEUS_OPERATOR_CRD_URL_PREFIX)/crd-alertmanagers.yaml' -O bin/configs/prometheus-crd-alertmanagers.yaml
	wget -q '$(PROMETHEUS_OPERATOR_CRD_URL_PREFIX)/crd-podmonitors.yaml' -O bin/configs/prometheus-crd-podmonitors.yaml
	wget -q '$(PROMETHEUS_OPERATOR_CRD_URL_PREFIX)/crd-probes.yaml' -O bin/configs/prometheus-crd-probes.yaml
	wget -q '$(PROMETHEUS_OPERATOR_CRD_URL_PREFIX)/crd-prometheuses.yaml' -O bin/configs/prometheus-crd-prometheuses.yaml
	wget -q '$(PROMETHEUS_OPERATOR_CRD_URL_PREFIX)/crd-prometheusrules.yaml' -O bin/configs/prometheus-crd-prometheusrules.yaml
	wget -q '$(PROMETHEUS_OPERATOR_CRD_URL_PREFIX)/crd-servicemonitors.yaml' -O bin/configs/prometheus-crd-servicemonitors.yaml
	wget -q '$(PROMETHEUS_OPERATOR_CRD_URL_PREFIX)/crd-thanosrulers.yaml' -O bin/configs/prometheus-crd-thanosrulers.yaml
	wget -q '$(CSI_SNAPSHOTTER_V3_CRD_URL_PREFIX)/snapshot.storage.k8s.io_volumesnapshots.yaml' -O bin/configs/csi-crd-v3-volumesnapshot.yaml
	wget -q '$(CSI_SNAPSHOTTER_V3_CRD_URL_PREFIX)/snapshot.storage.k8s.io_volumesnapshotcontents.yaml' -O bin/configs/csi-crd-v3-volumesnapshotcontent.yaml
	wget -q '$(CSI_SNAPSHOTTER_V3_CRD_URL_PREFIX)/snapshot.storage.k8s.io_volumesnapshotclasses.yaml' -O bin/configs/csi-crd-v3-volumesnapshotclass.yaml
	wget -q '$(CSI_SNAPSHOTTER_V4_CRD_URL_PREFIX)/snapshot.storage.k8s.io_volumesnapshots.yaml' -O bin/configs/csi-crd-v4-volumesnapshot.yaml
	wget -q '$(CSI_SNAPSHOTTER_V4_CRD_URL_PREFIX)/snapshot.storage.k8s.io_volumesnapshotcontents.yaml' -O bin/configs/csi-crd-v4-volumesnapshotcontent.yaml
	wget -q '$(CSI_SNAPSHOTTER_V4_CRD_URL_PREFIX)/snapshot.storage.k8s.io_volumesnapshotclasses.yaml' -O bin/configs/csi-crd-v4-volumesnapshotclass.yaml

clean-release-manifest:
	rm -rf manifests

get-release-manifest: clean-release-manifest
	mkdir -p manifests
	wget -q --no-check-certificate '$(PX_INSTALLER_HOST)/versions' -O manifests/portworx-releases-local.yaml

mockgen: $(GOPATH)/bin/gomock $(GOPATH)/bin/mockgen
	mockgen -destination=pkg/mock/openstoragesdk.mock.go -package=mock github.com/libopenstorage/openstorage/api OpenStorageRoleServer,OpenStorageNodeServer,OpenStorageClusterServer,OpenStorageNodeClient
	mockgen -destination=pkg/mock/storagedriver.mock.go -package=mock github.com/libopenstorage/operator/drivers/storage Driver
	mockgen -destination=pkg/mock/controllermanager.mock.go -package=mock sigs.k8s.io/controller-runtime/pkg/manager Manager
	mockgen -destination=pkg/mock/controller.mock.go -package=mock sigs.k8s.io/controller-runtime/pkg/controller Controller
	mockgen -destination=pkg/mock/controllercache.mock.go -package=mock sigs.k8s.io/controller-runtime/pkg/cache Cache

clean: clean-release-manifest clean-bundle
	@echo "Cleaning up binaries"
	@rm -rf $(BIN)
	@go clean -i $(PKGS)
	@echo "Deleting image "$(OPERATOR_IMG)
	@docker rmi -f $(OPERATOR_IMG)
