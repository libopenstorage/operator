FROM registry.access.redhat.com/rhel7-atomic:7.6

LABEL name="OpenStorage Operator" \
      vendor="" \
      version="v1.0.0" \
      release="1" \
      summary="OpenStorage Operator" \
      description="This will deploy an OpenStorage operator and manage StorageCluster pods in the cluster."

WORKDIR /

COPY licenses /licenses

COPY ./bin/operator /
