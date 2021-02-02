#!/bin/bash -x

test_image_name="openstorage/px-operator-test:latest"
default_portworx_spec_gen_url="https://install.portworx.com/"
portworx_endpoint=""
storage_provisioner="portworx"
focus_tests=""
short_test=false
portworx_docker_username=""
portworx_docker_password=""
log_level="debug"
for i in "$@"
do
case $i in
    --operator-test-image)
        echo "Operator Test Docker image to use for test: $2"
        test_image_name=$2
        shift
        shift
        ;;
    --portworx-docker-username)
        echo "Operator Docker username used to pull OCI image for test: $2"
        portworx_docker_username=$2
        shift
        shift
        ;;
    --portworx-docker-password)
        echo "Portworx Docker password used to pull OCI image for test: $2"
        portworx_docker_password=$2
        shift
        shift
        ;;
    --portworx-spec-gen-url)
        echo "Portworx Spec Generator URL to use to for test: $2"
        portworx_spec_gen_url=$2
        shift
        shift
        ;;
    --portworx-endpoint)
        echo "Portworx Spec Generator Endpoint to use to for test: $2"
        portworx_endpoint=$2
        shift
        shift
        ;;
    --storage-provisioner)
        echo "Storage provisioner to use for test: $2"
        storage_provisioner=$2
        shift
        shift
        ;;
    --focus-tests)
        echo "Flag for focus tests: $2"
        focus_tests=$2
        shift
        shift
        ;;
    --short-test)
        echo "Skip tests that are long/not supported: $2"
        short_test=$2
        shift
        shift
        ;;
    --log-level)
        echo "Log level for test: $2"
        log_level=$2
        shift
        shift
        ;;
esac
done

apk update
apk add jq

# Set log level
sed -i 's/'LOG_LEVEL'/'"$log_level"'/g' /testspecs/operator-test-pod.yaml

# Set Operator tests to execute
if [ "$focus_tests" != "" ]; then
     echo "Running focussed test: ${focus_tests}"
	sed -i 's|'FOCUS_TESTS'|- -test.run='"$focus_tests"'|g' /testspecs/operator-test-pod.yaml
else 
	sed -i 's|'FOCUS_TESTS'|''|g' /testspecs/operator-test-pod.yaml
fi

sed -i 's/'SHORT_FLAG'/'"$short_test"'/g' /testspecs/operator-test-pod.yaml


# Set Portworx Spec Generator URL
if [ "$portworx_spec_gen_url" == "" ]; then
	portworx_spec_gen_url=$default_portworx_spec_gen_url
fi
parsed_portworx_spec_gen_url=${portworx_spec_gen_url//[\/]/\\/} # This hack is needed because sed has issues with // and it throws an error
sed -i 's/'PORTWORX_SPEC_GEN_URL'/'"$parsed_portworx_spec_gen_url"'/g' /testspecs/operator-test-pod.yaml

# Set Portworx endpoint
if [ "$portworx_endpoint" != "" ]; then
	echo "Portworx endpoint: $portworx_endpoint"
else
	echo "Portworx endpoint is empty, will use default endpoint for $portworx_spec_gen_url"
fi
sed -i 's/'PORTWORX_ENDPOINT'/'"$portworx_endpoint"'/g' /testspecs/operator-test-pod.yaml

# Portworx Docker credentials
if [ "$portworx_docker_username" != "" ] && [ "$portworx_docker_password" != "" ]; then
	sed -i 's/'PORTWORX_DOCKER_USERNAME'/'"$portworx_docker_username"'/g' /testspecs/operator-test-pod.yaml
	sed -i 's/'PORTWORX_DOCKER_PASSWORD'/'"$portworx_docker_password"'/g' /testspecs/operator-test-pod.yaml
else
    sed -i 's|'PORTWORX_DOCKER_USERNAME'|''|g' /testspecs/operator-test-pod.yaml
	sed -i 's|'PORTWORX_DOCKER_PASSWORD'|''|g' /testspecs/operator-test-pod.yaml
fi

# Set test image
sed -i 's|'openstorage/px-operator-test:.*'|'"$test_image_name"'|g'  /testspecs/operator-test-pod.yaml

kubectl delete -f /testspecs/operator-test-pod.yaml
kubectl create -f /testspecs/operator-test-pod.yaml

for i in $(seq 1 100) ; do
    test_status=$(kubectl -n kube-system get pod operator-test -o json | jq ".status.phase" -r)
    if [ "$test_status" == "Running" ] || [ "$test_status" == "Completed" ]; then
        break
    else
        echo "Test hasn't started yet, status: $test_status"
        sleep 5
    fi
done

kubectl -n kube-system logs -f operator-test
for i in $(seq 1 100) ; do
    test_status=$(kubectl -n kube-system get pod operator-test -o json | jq ".status.phase" -r)
    if [ "$test_status" == "Running" ]; then
        echo "Test is still running, status: $test_status"
        kubectl -n kube-system logs -f operator-test
    else
        sleep 5
        break
    fi
done

test_status=$(kubectl -n kube-system get pod operator-test -o json | jq ".status.phase" -r)
if [ "$test_status" == "Succeeded" ]; then
    echo "Tests passed"
    exit 0
elif [ "$test_status" == "Failed" ]; then
    echo "Tests failed"
    exit 1
else
    echo "Unknown test status $test_status"
    exit 1
fi
