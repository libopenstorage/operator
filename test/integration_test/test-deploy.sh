#!/bin/bash -x

test_pod_template="/testspecs/operator-test-pod-template.yaml"
test_pod_spec="/testspecs/operator-test-pod.yaml"

test_image_name="openstorage/px-operator-test:latest"
default_portworx_spec_gen_url="https://install.portworx.com/"
upgrade_hops_url_list=""
focus_tests=""
short_test=false
portworx_docker_username=""
portworx_docker_password=""
portworx_image_override=""
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
    --portworx-image-override)
        echo "Portworx Image to use for test: $2"
        portworx_image_override=$2
        shift
        shift
        ;;
    --upgrade-hops-url-list)
        echo "List of Portworx Spec Generator URLs to use as Upgrade hops for test: $2"
        upgrade_hops_url_list=$2
        shift
        shift
        ;;
    --focus-tests)
        echo "Flag for focus tests: $2"
        focus_tests=$2
        shift
        shift
        ;;
<<<<<<< HEAD
=======
    --cloud-provider)
        echo "Flag for cloud provider type: $2"
        cloud_provider=$2
        shift
        shift
        ;;
    --is-ocp)
        echo "Flag for OCP: $2"
        is_ocp=$2
        shift
        shift
        ;;
    --is-eks)
        echo "Flag for EKS: $2"
        is_eks=$2
        shift
        shift
        ;;
    --is-aks)
        echo "Flag for AKS: $2"
        is_aks=$2
        shift
        shift
        ;;
    --is-gke)
        echo "Flag for GKE: $2"
        is_gke=$2
        shift
        shift
        ;;
    --is-oke)
        echo "Flag for OKE: $2"
        is_oke=$2
        shift
        shift
        ;;
    --portworx-device-specs)
        echo "Flag for Portworx device specs: $2"
        portworx_device_specs=$2
        shift
        shift
        ;;
    --portworx-kvdb-spec)
        echo "Flag for Portworx KVDB device spec: $2"
        portworx_kvdb_spec=$2
        shift
        shift
        ;;
    --portworx-env-vars)
        echo "Flag for Portworx ENV vars: $2"
        portworx_env_vars=$2
        shift
        shift
        ;;
>>>>>>> 5d22a47b... [PWX-28269] Added support for `portworx.io/is-oke=true` annotation (#864)
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

# Copy test pod template to a new file
cp $test_pod_template $test_pod_spec

# Set log level
sed -i 's|'LOG_LEVEL'|'"$log_level"'|g' $test_pod_spec

# Set Operator tests to execute
if [ "$focus_tests" != "" ]; then
    echo "Running focussed test: ${focus_tests}"
    sed -i 's|'FOCUS_TESTS'|'"$focus_tests"'|g' $test_pod_spec
else
    sed -i 's|'"-\s-test.run=FOCUS_TESTS"'|''|g' $test_pod_spec
fi

sed -i 's|'SHORT_FLAG'|'"$short_test"'|g' $test_pod_spec

<<<<<<< HEAD
=======
# Cloud provider
if [ "$cloud_provider" != "" ]; then
	echo "Cloud provider: $cloud_provider"
	sed -i 's|'CLOUD_PROVIDER'|'"$cloud_provider"'|g' $test_pod_spec
else
	sed -i '/CLOUD_PROVIDER/d' $test_pod_spec
fi

# Portworx device specs
if [ "$portworx_device_specs" != "" ]; then
    echo "Portworx volumes: $portworx_device_specs"
    sed -i 's|'PORTWORX_DEVICE_SPECS'|'"$portworx_device_specs"'|g' $test_pod_spec
else
    sed -i '/PORTWORX_DEVICE_SPECS/d' $test_pod_spec
fi

# Portworx KVDB device spec
if [ "$portworx_kvdb_spec" != "" ]; then
    echo "Portworx KVDB: $portworx_kvdb_spec"
    sed -i 's|'PORTWORX_KVDB_SPEC'|'"$portworx_kvdb_spec"'|g' $test_pod_spec
else
    sed -i '/PORTWORX_KVDB_SPEC/d' $test_pod_spec
fi

# Portworx ENV vars
if [ "$portworx_env_vars" != "" ]; then
    echo "Portworx ENV vars: $portworx_env_vars"
    sed -i 's|'PORTWORX_ENV_VARS'|'"$portworx_env_vars"'|g' $test_pod_spec
else
    sed -i '/PORTWORX_ENV_VARS/d' $test_pod_spec
fi

# Set OCP
if [ "$is_ocp" != "" ]; then
	echo "This is OCP cluster: $is_ocp"
	sed -i 's|'IS_OCP'|'"$is_ocp"'|g' $test_pod_spec
else
	sed -i 's|'IS_OCP'|''|g' $test_pod_spec
fi

# Set EKS
if [ "$is_eks" != "" ]; then
    echo "This is EKS cluster: $is_eks"
    sed -i 's|'IS_EKS'|'"$is_eks"'|g' $test_pod_spec
else
    sed -i 's|'IS_EKS'|''|g' $test_pod_spec
fi

# Set AKS
if [ "$is_aks" != "" ]; then
    echo "This is AKS cluster: $is_aks"
    sed -i 's|'IS_AKS'|'"$is_aks"'|g' $test_pod_spec
else
    sed -i 's|'IS_AKS'|''|g' $test_pod_spec
fi

# Set GKE
if [ "$is_gke" != "" ]; then
    echo "This is GKE cluster: $is_gke"
    sed -i 's|'IS_GKE'|'"$is_gke"'|g' $test_pod_spec
else
    sed -i 's|'IS_GKE'|''|g' $test_pod_spec
fi

# Set OKE
if [ "$is_oke" != "" ]; then
    echo "This is OKE cluster: $is_oke"
    sed -i 's|'IS_OKE'|'"$is_oke"'|g' $test_pod_spec
else
    sed -i 's|'IS_OKE'|''|g' $test_pod_spec
fi

>>>>>>> 5d22a47b... [PWX-28269] Added support for `portworx.io/is-oke=true` annotation (#864)
# Set Portworx Spec Generator URL
if [ "$portworx_spec_gen_url" == "" ]; then
    portworx_spec_gen_url=$default_portworx_spec_gen_url
fi
sed -i 's|'PORTWORX_SPEC_GEN_URL'|'"$portworx_spec_gen_url"'|g' $test_pod_spec

# Upgrade hops URL list
if [ "$upgrade_hops_url_list" != "" ]; then
    sed -i 's|'UPGRADE_HOPS_URL_LIST'|'"$upgrade_hops_url_list"'|g' $test_pod_spec
else
    sed -i 's|'UPGRADE_HOPS_URL_LIST'|''|g' $test_pod_spec
fi

# Portworx Docker credentials
if [ "$portworx_docker_username" != "" ] && [ "$portworx_docker_password" != "" ]; then
    sed -i 's|'PORTWORX_DOCKER_USERNAME'|'"$portworx_docker_username"'|g' $test_pod_spec
    sed -i 's|'PORTWORX_DOCKER_PASSWORD'|'"$portworx_docker_password"'|g' $test_pod_spec
else
    sed -i 's|'PORTWORX_DOCKER_USERNAME'|''|g' $test_pod_spec
    sed -i 's|'PORTWORX_DOCKER_PASSWORD'|''|g' $test_pod_spec
fi

# Set test image
sed -i 's|'openstorage/px-operator-test:.*'|'"$test_image_name"'|g' $test_pod_spec
sed -i 's|'PX_IMAGE_OVERRIDE'|'"$portworx_image_override"'|g' $test_pod_spec

kubectl delete -f $test_pod_template
kubectl create -f $test_pod_spec

for i in $(seq 1 100) ; do
    test_status=$(kubectl -n kube-system get pod operator-test -o json | jq ".status.phase" -r)
    if [ "$test_status" == "Running" ] || [ "$test_status" == "Succeeded" ]; then
        break
    elif [ "$test_status" == "Failed" ]; then
        kubectl -n kube-system logs operator-test

        echo ""
        echo "Tests failed"
        exit 1
    else
        echo "Test hasn't started yet, status: $test_status"
        sleep 5
    fi
done

kubectl -n kube-system logs -f operator-test
for i in $(seq 1 100) ; do
    sleep 5  # Give the test pod a chance to finish first after the logs stop
    test_status=$(kubectl -n kube-system get pod operator-test -o json | jq ".status.phase" -r)
    if [ "$test_status" == "Running" ]; then
        echo "Test is still running, status: $test_status"
        kubectl -n kube-system logs -f operator-test
    else
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
