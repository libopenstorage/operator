# set defaults
ifndef DOCKER_HUB_RESOURCE_GATEWAY_PROTO_IMG
    DOCKER_HUB_RESOURCE_GATEWAY_PROTO_IMG := resource-gateway-proto
    $(warning DOCKER_HUB_RESOURCE_GATEWAY_PROTO_IMG not defined, using '$(DOCKER_HUB_RESOURCE_GATEWAY_PROTO_IMG)' instead)
endif
ifndef DOCKER_HUB_RESOURCE_GATEWAY_PROTO_TAG
    DOCKER_HUB_RESOURCE_GATEWAY_PROTO_TAG := latest
    $(warning DOCKER_HUB_RESOURCE_GATEWAY_PROTO_TAG not defined, using '$(DOCKER_HUB_RESOURCE_GATEWAY_PROTO_TAG)' instead)
endif

ifndef PROTOC
PROTOC = protoc
endif

ifndef PROTOS_PATH
PROTOS_PATH = $(GOPATH)/src
endif

ifndef PROTOSRC_PATH
PROTOSRC_PATH = $(PROTOS_PATH)/github.com/libopenstorage/operator/proto
endif

RESOURCE_GATEWAY_PROTO_IMG=$(DOCKER_HUB_REPO)/$(DOCKER_HUB_RESOURCE_GATEWAY_PROTO_IMG):$(DOCKER_HUB_RESOURCE_GATEWAY_PROTO_TAG)

# builds the container to compile proto files
build-docker:
	docker build -t $(RESOURCE_GATEWAY_PROTO_IMG) .

# builds the proto target inside the build container
docker-proto:
	docker run \
		--privileged --rm -it \
		-v $(shell pwd):/go/src/github.com/libopenstorage/operator/proto \
		-e "GOPATH=/go" \
		-e "DOCKER_PROTO=yes" \
		-e "PATH=/bin:/usr/bin:/usr/local/bin:/go/bin:/usr/local/go/bin" \
		$(RESOURCE_GATEWAY_PROTO_IMG) \
		make proto

# compiles the proto files - should be run inside the build container
proto: $(GOPATH)/bin/protoc-gen-go $(GOPATH)/bin/protoc-gen-grpc-gateway $(GOPATH)/bin/protoc-gen-swagger
ifndef DOCKER_PROTO
	$(error Do not run directly. Run 'make docker-proto' instead.)
endif
	@echo ">>> Generating protobuf definitions from api/api.proto"
	$(PROTOC) -I $(PROTOSRC_PATH) \
		-I /usr/local/include \
		-I $(PROTOS_PATH)/github.com/grpc-ecosystem/grpc-gateway/third_party/googleapis \
		--go_out=plugins=grpc:. \
		$(PROTOSRC_PATH)/resource_gateway.proto

clean:
	rm -f resource_gateway.pb.go