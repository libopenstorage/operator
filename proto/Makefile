# set defaults
ifndef DOCKER_HUB_PX_RESOURCE_GATEWAY_PROTO_IMG
    DOCKER_HUB_PX_RESOURCE_GATEWAY_PROTO_IMG := px-resource-gateway-proto
    $(warning DOCKER_HUB_PX_RESOURCE_GATEWAY_PROTO_IMG not defined, using '$(DOCKER_HUB_PX_RESOURCE_GATEWAY_PROTO_IMG)' instead)
endif
ifndef DOCKER_HUB_PX_RESOURCE_GATEWAY_PROTO_TAG
    DOCKER_HUB_PX_RESOURCE_GATEWAY_PROTO_TAG := latest
    $(warning DOCKER_HUB_PX_RESOURCE_GATEWAY_PROTO_TAG not defined, using '$(DOCKER_HUB_PX_RESOURCE_GATEWAY_PROTO_TAG)' instead)
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

PX_RESOURCE_GATEWAY_PROTO_IMG=$(DOCKER_HUB_REPO)/$(DOCKER_HUB_PX_RESOURCE_GATEWAY_PROTO_IMG):$(DOCKER_HUB_PX_RESOURCE_GATEWAY_PROTO_TAG)

px-resource-gateway-proto-build-docker-proto:
	docker build -t $(PX_RESOURCE_GATEWAY_PROTO_IMG) .

px-resource-gateway-docker-proto:
	docker run \
		--privileged --rm -it \
		-v $(shell pwd):/go/src/github.com/libopenstorage/operator/proto \
		-e "GOPATH=/go" \
		-e "DOCKER_PROTO=yes" \
		-e "PATH=/bin:/usr/bin:/usr/local/bin:/go/bin:/usr/local/go/bin" \
		$(PX_RESOURCE_GATEWAY_PROTO_IMG) \
		make px-resource-gateway-proto

px-resource-gateway-proto: $(GOPATH)/bin/protoc-gen-go $(GOPATH)/bin/protoc-gen-grpc-gateway $(GOPATH)/bin/protoc-gen-swagger
ifndef DOCKER_PROTO
	$(error Do not run directly. Run 'make docker-proto' instead.)
endif
	@echo ">>> Generating protobuf definitions from api/api.proto"
	$(PROTOC) -I $(PROTOSRC_PATH) \
		-I /usr/local/include \
		-I $(PROTOS_PATH)/github.com/grpc-ecosystem/grpc-gateway/third_party/googleapis \
		--go_out=plugins=grpc:. \
		$(PROTOSRC_PATH)/pxresourcegateway.proto
	$(PROTOC) -I $(PROTOSRC_PATH) \
		-I /usr/local/include \
		-I $(PROTOS_PATH)/github.com/grpc-ecosystem/grpc-gateway/third_party/googleapis \
		--grpc-gateway_out=logtostderr=true:. \
		$(PROTOSRC_PATH)/pxresourcegateway.proto
	$(PROTOC) -I $(PROTOSRC_PATH) \
		-I /usr/local/include \
		-I $(PROTOS_PATH)/github.com/grpc-ecosystem/grpc-gateway/third_party/googleapis \
		--swagger_out=logtostderr=true:$(PROTOSRC_PATH) \
		$(PROTOSRC_PATH)/pxresourcegateway.proto
	@echo ">>> Upgrading swagger 2.0 to openapi 3.0"
	mv pxresourcegateway.swagger.json 20pxresourcegateway.swagger.json
	swagger2openapi 20pxresourcegateway.swagger.json -o pxresourcegateway.swagger.json
	rm -f 20pxresourcegateway.swagger.json

clean:
	rm -f pxresourcegateway.pb.go pxresourcegateway.pb.gw.go pxresourcegateway.swagger.json
	rm -rf proto/