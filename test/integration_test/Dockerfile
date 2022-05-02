FROM golang:1.16.10

# Install dependancies
RUN apt-get update && \ 
    /usr/local/go/bin/go install gotest.tools/gotestsum@latest

WORKDIR /

COPY operator.test /
COPY testspec /testspec
