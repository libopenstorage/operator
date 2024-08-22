# This Dockerfile is used to build base image for Resource Gateway proto files
# The image is based on osd-proto image provided by OpenStorage

FROM quay.io/openstorage/osd-proto-clients

LABEL author="dgoel"

RUN mkdir -p /go/src/github.com/libopenstorage/operator/proto

WORKDIR /go/src/github.com/libopenstorage/operator/proto
