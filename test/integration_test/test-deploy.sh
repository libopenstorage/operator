#!/bin/bash -x

test_image_name="openstorage/px-operator-test:latest"
environment_variables=""
storage_provisioner="portworx"
focus_tests=""
auth_secret_configmap=""
short_test=false
cloud_secret=""
aws_id=""
aws_key=""
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
        echo "Flag for clusterdomain test: $2"
        storage_provisioner=$2
        shift
        shift
        ;;
    --env_vars)
        echo "Flag for environment variables: $2"
        environment_variables=$2
        shift
        shift
        ;;
    --focus_tests)
        echo "Flag for focus tests: $2"
        focus_tests=$2
        shift
        shift
        ;;
    --auth_secret_configmap)
        echo "K8s secret name to use for test: $2"
        auth_secret_configmap=$2
        shift
        shift
        ;;
    --short_test)
        echo "Skip tests that are long/not supported: $2"
        short_test=$2
        shift
        shift
        ;;
    --cloud_secret)
        echo "Secret name for cloud provider API access: $2"
        cloud_secret=$2
        shift
        shift
        ;;
    --aws_id)
        echo "AWS user id for API access: $2"
        aws_id=$2
        shift
        shift
        ;;
    --aws_key)
        echo "AWS key for API access: $2"
        aws_key=$2
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

# Configmap with secrets indicates auth-enabled runs are required
#  * Adding the shared secret to operator-test pod as env variable
#  * Pass config map to containing px secret operator-test pod so tests can pick up auth-token
if [ "$auth_secret_configmap" != "" ] ; then
    sed -i 's/'auth_secret_configmap'/'"$auth_secret_configmap"'/g' /testspecs/operator-test-pod.yaml
    sed -i 's/'px_shared_secret_key'/'"$AUTH_SHARED_KEY"'/g' /testspecs/operator-test-pod.yaml
else
	sed -i 's/'auth_secret_configmap'/'\"\"'/g' /testspecs/operator-test-pod.yaml
fi

sed -i 's/'storage_provisioner'/'"$storage_provisioner"'/g' /testspecs/operator-test-pod.yaml
sed -i 's/'username'/'"$SSH_USERNAME"'/g' /testspecs/operator-test-pod.yaml
sed -i 's/'password'/'"$SSH_PASSWORD"'/g' /testspecs/operator-test-pod.yaml
sed  -i 's|'openstorage/operator-test:.*'|'"$test_image_name"'|g'  /testspecs/operator-test-pod.yaml

# Add AWS creds to operator-test pod
sed -i 's/'aws_access_key_id'/'"$aws_id"'/g' /testspecs/operator-test-pod.yaml
sed -i 's/'aws_secret_access_key'/'"$aws_key"'/g' /testspecs/operator-test-pod.yaml

kubectl delete -f /testspecs/operator-test-pod.yaml
kubectl create -f /testspecs/operator-test-pod.yaml

for i in $(seq 1 100) ; do
    test_status=$(kubectl get pod operator-test -n kube-system -o json | jq ".status.phase" -r)
    if [ "$test_status" == "Running" ] || [ "$test_status" == "Completed" ]; then
        break
    else
        echo "Test hasn't started yet, status: $test_status"
        sleep 5
    fi
done

kubectl logs operator-test  -n kube-system -f
for i in $(seq 1 100) ; do
    test_status=$(kubectl get pod operator-test -n kube-system -o json | jq ".status.phase" -r)
    if [ "$test_status" == "Running" ]; then
        echo "Test is still running, status: $test_status"
        kubectl logs operator-test  -n kube-system -f
    else
        sleep 5
        break
    fi
done

test_status=$(kubectl get pod operator-test -n kube-system -o json | jq ".status.phase" -r)
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
