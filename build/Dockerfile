FROM registry.access.redhat.com/ubi8-minimal:latest

RUN microdnf update
RUN microdnf --enablerepo=rhel-7-server-rpms \
-y update-minimal --security --sec-severity=Important --sec-severity=Critical;\
microdnf clean all
RUN microdnf install -y tar

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
