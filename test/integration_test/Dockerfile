FROM golang:1.21

# Install dependancies
RUN apt-get update && \ 
    apt-get install -y jq file
RUN /usr/local/go/bin/go install gotest.tools/gotestsum@latest

# Install aws-iam-authenticator
# This is needed by test running inside EKS cluster and making API calls to aws.
RUN curl -fsSL -o aws-iam-authenticator https://amazon-eks.s3.us-west-2.amazonaws.com/1.21.2/2021-07-05/bin/linux/amd64/aws-iam-authenticator && \
    (file aws-iam-authenticator | grep -q "ELF 64-bit LSB executable") && \
    chmod a+x aws-iam-authenticator && \
    mv aws-iam-authenticator /bin/

# Install kubectl
RUN curl -fsSL -o kubectl "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && \
    (file kubectl | grep -q "ELF 64-bit LSB executable") && \
    chmod +x ./kubectl && \
    mv ./kubectl /usr/local/bin/

WORKDIR /

COPY operator.test /
COPY testspec /testspec
COPY test-deploy.sh /
COPY operator-test-pod-template.yaml /
