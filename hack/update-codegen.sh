#!/bin/bash

# Copyright 2018 Openstorage.org
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT=$(dirname ${BASH_SOURCE})/..
CODEGEN_PKG=${CODEGEN_PKG:-$(cd ${SCRIPT_ROOT}; ls -d -1 ./vendor/k8s.io/code-generator 2>/dev/null || echo ../code-generator)}


# Pull in go.mod hack from https://github.com/openshift/client-go/blob/master/hack/update-codegen.sh
# we have to fake the vendor for generator in order to be able to build it...
mv ${CODEGEN_PKG}/generate-groups.sh ${CODEGEN_PKG}/generate-groups.sh.orig
sed 's/ go install/#go install/g' ${CODEGEN_PKG}/generate-groups.sh.orig > ${CODEGEN_PKG}/generate-groups.sh
chmod +x ${CODEGEN_PKG}/generate-groups.sh
function cleanup {
  mv ${CODEGEN_PKG}/generate-groups.sh.orig ${CODEGEN_PKG}/generate-groups.sh
}
trap cleanup EXIT

go install ./${CODEGEN_PKG}/cmd/{defaulter-gen,client-gen,lister-gen,informer-gen,deepcopy-gen}


# generate the code with:
bash ${CODEGEN_PKG}/generate-groups.sh \
	all \
  github.com/libopenstorage/operator/pkg/client \
	github.com/libopenstorage/operator/pkg/apis \
  "core:v1alpha1" \
  --go-header-file ${SCRIPT_ROOT}/hack/custom-boilerplate.go.txt
