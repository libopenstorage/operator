module github.com/libopenstorage/operator

go 1.15

require (
	github.com/dgrijalva/jwt-go v3.2.0+incompatible
	github.com/emicklei/go-restful v2.11.1+incompatible // indirect
	github.com/frankban/quicktest v1.4.1 // indirect
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32 // indirect
	github.com/go-logr/logr v0.3.0
	github.com/go-test/deep v1.0.4 // indirect
	github.com/golang/mock v1.5.0
	github.com/google/shlex v0.0.0-20181106134648-c34317bd91bf
	github.com/hashicorp/go-retryablehttp v0.6.6 // indirect
	github.com/hashicorp/go-version v1.2.1
	github.com/hashicorp/vault/api v1.0.5-0.20200317185738-82f498082f02 // indirect
	github.com/hashicorp/vault/sdk v0.1.14-0.20200429182704-29fce8f27ce4 // indirect
	github.com/kubernetes-csi/external-snapshotter/v2 v2.1.1 // indirect
	github.com/libopenstorage/cloudops v0.0.0-20200604165016-9cc0977d745e
	github.com/libopenstorage/openstorage v8.0.1-0.20200914191223-6fb8d163a67c+incompatible
	github.com/libopenstorage/stork v1.3.0-beta1.0.20210415204554-6b98cc805c5f // indirect
	github.com/pierrec/lz4 v2.5.2+incompatible // indirect
	github.com/portworx/kvdb v0.0.0-20200723230726-2734b7f40194
	github.com/portworx/px-backup-api v1.2.1-0.20210416161003-f19256c6e2c5 // indirect
	github.com/portworx/sched-ops v1.20.4-rc1.0.20210407163031-09e9dcbb0f2f
	github.com/portworx/torpedo v0.20.4-rc1.0.20210325154352-eb81b0cdd145
	github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring v0.46.0
	github.com/prometheus-operator/prometheus-operator/pkg/client v0.46.0
	github.com/sirupsen/logrus v1.7.0
	github.com/stretchr/testify v1.7.0
	github.com/urfave/cli v1.22.2
	google.golang.org/grpc v1.36.1
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/api v0.20.4
	k8s.io/apiextensions-apiserver v0.20.4
	k8s.io/apimachinery v0.20.4
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/code-generator v0.20.4
	k8s.io/klog v1.0.0
	k8s.io/kube-openapi v0.0.0-20210216185858-15cd8face8d6 // indirect
	k8s.io/kubernetes v1.20.4
	k8s.io/utils v0.0.0-20201110183641-67b214c5f920
	sigs.k8s.io/cluster-api v0.2.11
	sigs.k8s.io/controller-runtime v0.8.0
)

replace (
	github.com/Azure/azure-sdk-for-go => github.com/Azure/azure-sdk-for-go v43.0.0+incompatible
	github.com/coreos/prometheus-operator => github.com/prometheus-operator/prometheus-operator v0.46.0
	github.com/hashicorp/consul => github.com/hashicorp/consul v1.5.1
	github.com/kubernetes-incubator/external-storage => github.com/libopenstorage/external-storage v0.20.4-openstorage-rc3
	github.com/kubernetes-incubator/external-storage v0.0.0-00010101000000-000000000000 => github.com/libopenstorage/external-storage v5.3.0-alpha.1.0.20200130041458-d2b33d4448ea+incompatible
	github.com/libopenstorage/autopilot-api => github.com/libopenstorage/autopilot-api v0.6.1-0.20210301232050-ca2633c6e114
	google.golang.org/grpc => google.golang.org/grpc v1.29.1
	google.golang.org/grpc/examples/helloworld/helloworld => google.golang.org/grpc/examples/helloworld/helloworld v1.29.1
	gopkg.in/fsnotify.v1 v1.4.7 => github.com/fsnotify/fsnotify v1.4.7
	k8s.io/api => k8s.io/api v0.20.4
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.20.4
	k8s.io/apimachinery => k8s.io/apimachinery v0.20.4
	k8s.io/apiserver => k8s.io/apiserver v0.20.4
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.20.4
	k8s.io/client-go => k8s.io/client-go v0.20.4
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.20.4
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.20.4
	k8s.io/code-generator => k8s.io/code-generator v0.20.4
	k8s.io/component-base => k8s.io/component-base v0.20.4
	k8s.io/component-helpers => k8s.io/component-helpers v0.20.4
	k8s.io/controller-manager => k8s.io/controller-manager v0.20.4
	k8s.io/cri-api => k8s.io/cri-api v0.20.4
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.20.4
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.20.4
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.20.4
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.20.4
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.20.4
	k8s.io/kubectl => k8s.io/kubectl v0.20.4
	k8s.io/kubelet => k8s.io/kubelet v0.20.4
	k8s.io/kubernetes => k8s.io/kubernetes v1.20.4
	k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.20.4
	k8s.io/metrics => k8s.io/metrics v0.20.4
	k8s.io/mount-utils => k8s.io/mount-utils v0.20.4
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.20.4
)
