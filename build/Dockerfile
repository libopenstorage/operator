FROM registry.access.redhat.com/ubi9-minimal:latest

RUN microdnf clean all && \
    microdnf install -y tar && \
    microdnf clean all

USER nobody

LABEL name="OpenStorage Operator" \
      vendor="openstorage.org" \
      version="v1.0.0" \
      release="1" \
      summary="OpenStorage Operator" \
      description="This will deploy an OpenStorage operator and manage StorageCluster pods in the cluster."

WORKDIR /

COPY licenses /licenses
COPY vendor/github.com/libopenstorage/cloudops/specs /specs
COPY deploy/crds /crds
COPY manifests /manifests
COPY bin/configs /configs
COPY bin/operator /
COPY bin/dryrun /
