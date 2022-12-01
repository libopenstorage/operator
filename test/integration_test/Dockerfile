FROM golang:1.16.10

# Install dependancies
RUN apt-get update && \ 
    /usr/local/go/bin/go install gotest.tools/gotestsum@latest

# Install aws-iam-authenticator
# This is needed by test running inside EKS cluster and making API calls to aws.
RUN curl -o aws-iam-authenticator https://amazon-eks.s3.us-west-2.amazonaws.com/1.21.2/2021-07-05/bin/linux/amd64/aws-iam-authenticator && \
    chmod a+x aws-iam-authenticator && \
    mv aws-iam-authenticator /bin

WORKDIR /

COPY operator.test /
COPY testspec /testspec
COPY test-deploy.sh /
