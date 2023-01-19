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

If you get errors like 

```sh
/usr/local/go/src/math/erf.go:189:6: Erf defined in both Go and assembly
/usr/local/go/src/math/erf.go:274:6: Erfc defined in both Go and assembly
```


try to switch to go 1.18. 

Make sure not only $GOPATH but also $GOPATH/bin are in your system $PATH, otherwise `staticcheck` would fail.

## Build operator container

Set environment variables

```sh
export DOCKER_HUB_REPO=<eg. mybuildx>
```

## Build container and push container

```sh
make container deploy
```
