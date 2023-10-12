FROM golang:1.18

# Install dependancies
RUN apt-get update
RUN /usr/local/go/bin/go install gotest.tools/gotestsum@latest
RUN apt-get install -y jq

# Install aws-iam-authenticator
# This is needed by test running inside EKS cluster and making API calls to aws.
RUN curl -o aws-iam-authenticator https://amazon-eks.s3.us-west-2.amazonaws.com/1.21.2/2021-07-05/bin/linux/amd64/aws-iam-authenticator && \
    chmod a+x aws-iam-authenticator && \
    mv aws-iam-authenticator /bin

# Install kubectl
RUN curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
RUN chmod +x ./kubectl
RUN mv ./kubectl /usr/local/bin

WORKDIR /

COPY operator.test /
COPY testspec /testspec
COPY test-deploy.sh /
COPY operator-test-pod-template.yaml /
