[![Build Status](https://app.travis-ci.com/libopenstorage/operator.svg?branch=master)](https://app.travis-ci.com/libopenstorage/operator)
[![Code Coverage](https://codecov.io/gh/libopenstorage/operator/branch/master/graph/badge.svg)](https://codecov.io/gh/libopenstorage/operator)

# Openstorage Operator
Operator to manage storage cluster in Kubernetes

# Development

## Clone the operator repository

```sh
git clone git@github.com:libopenstorage/operator.git
cd operator
```
## Build operator

```sh
make downloads all
```

Troubleshooting:

Do not modify `$GOBIN` -- this build process depends on installing and using linter-tools (i.e. `staticcheck`) from the default `$GOPATH/bin`.


Q: I'm getting the following warning during the build:

```
WARNING: Tool /go/bin/my-linter-tool compiled with go1.20.10	 (you are using go1.21.4)
```

A: To fix this, just remove the binary, and the build-process should automatically rebuild the tool using your current golang compiler.

## Build operator container

Set environment variables

```sh
export DOCKER_HUB_REPO=<eg. mybuildx>
```

## Build container and push container

```sh
make container deploy
```
