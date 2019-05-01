FROM registry.access.redhat.com/rhel7-atomic

LABEL name="OpenStorage Operator" \
      vendor="" \
      version="v0.1.0" \
      release="1" \
      summary="OpenStorage Operator" \
      description="This operator will deploy 3 instances of OpenStorage operator in the cluster."

WORKDIR /

COPY licenses /licenses

COPY ./bin/operator /
