#!/bin/bash -x

test_image_name="openstorage/px-operator-test:latest"
storage_provisioner="portworx"
focus_tests=""
short_test=false
for i in "$@"
do
case $i in
    --operator-test-image)
        echo "Operator Test Docker image to use for test: $2"
        test_image_name=$2
        shift
        shift
        ;;
    --storage_provisioner)
        echo "Storage provisioner to use for test: $2"
        storage_provisioner=$2
        shift
        shift
        ;;
    --focus_tests)
        echo "Flag for focus tests: $2"
        focus_tests=$2
        shift
        shift
        ;;
    --short_test)
        echo "Skip tests that are long/not supported: $2"
        short_test=$2
        shift
        shift
        ;;
esac
done

apk update
apk add jq
apt-get -y update 
apt-get -y install jq

# Add operator tests to execute
if [ "$focus_tests" != "" ] ; then
     echo "Running focussed test: ${focus_tests}"
	sed -i 's|'FOCUS_TESTS'|- -test.run='"$focus_tests"'|g' /testspecs/operator-test-pod.yaml
else 
	sed -i 's|'FOCUS_TESTS'|''|g' /testspecs/operator-test-pod.yaml
fi

sed -i 's/'SHORT_FLAG'/'"$short_test"'/g' /testspecs/operator-test-pod.yaml

sed -i 's/'storage_provisioner'/'"$storage_provisioner"'/g' /testspecs/operator-test-pod.yaml
sed -i 's/'username'/'"$SSH_USERNAME"'/g' /testspecs/operator-test-pod.yaml
sed -i 's/'password'/'"$SSH_PASSWORD"'/g' /testspecs/operator-test-pod.yaml
sed -i 's|'openstorage/operator-test:.*'|'"$test_image_name"'|g'  /testspecs/operator-test-pod.yaml

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
