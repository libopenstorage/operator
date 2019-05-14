STORAGE_OPERATOR_IMG=$(DOCKER_HUB_REPO)/$(DOCKER_HUB_STORAGE_OPERATOR_IMAGE):$(DOCKER_HUB_STORAGE_OPERATOR_TAG)

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

RELEASE_VER := 1.0.3
BASE_DIR    := $(shell git rev-parse --show-toplevel)
GIT_SHA     := $(shell git rev-parse --short HEAD)
BIN         := $(BASE_DIR)/bin

VERSION = $(RELEASE_VER)-$(GIT_SHA)

LDFLAGS += "-s -w -X github.com/libopenstorage/operator/pkg/version.Version=$(VERSION)"
BUILD_OPTIONS := -ldflags=$(LDFLAGS)

.DEFAULT_GOAL=all
.PHONY: deploy clean vendor vendor-update

all: operator pretest

vendor-update:
	dep ensure -update

vendor:
	dep ensure

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
		go test -v -coverprofile=profile.out -covermode=atomic $${pkg} || exit 1; \
		if [ -f profile.out ]; then \
			cat profile.out >> coverage.txt; \
			rm profile.out; \
		fi; \
	done

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

olm-verify:
	docker run -it --rm -v $(BASE_DIR)/deploy/olm-catalog/portworx/$(RELEASE_VER):/portworx \
		python:3 bash -c "pip3 install operator-courier && operator-courier --verbose verify --ui_validate_io /portworx"

clean:
	-rm -rf $(BIN)
	@echo "Deleting image "$(STORAGE_OPERATOR_IMG)
	-sudo docker rmi -f $(STORAGE_OPERATOR_IMG)
	go clean -i $(PKGS)
