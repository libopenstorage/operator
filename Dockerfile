FROM registry.access.redhat.com/rhel7-atomic
MAINTAINER Portworx Inc. <support@portworx.com>

# RUN apk add --update bash

WORKDIR /

COPY ./bin/operator /
