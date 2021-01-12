FROM golang:1.15.1

# Install dependancies
RUN apt-get update && \ 
    /usr/local/go/bin/go get -u gotest.tools/gotestsum

WORKDIR /

COPY operator.test /
COPY specs /specs/
