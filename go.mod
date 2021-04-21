module github.com/libopenstorage/operator

go 1.15

require (
	github.com/dgrijalva/jwt-go v3.2.0+incompatible
	github.com/go-logr/logr v0.3.0
	github.com/golang/mock v1.5.0
	github.com/google/shlex v0.0.0-20181106134648-c34317bd91bf
	github.com/grpc-ecosystem/go-grpc-middleware v1.2.2 // indirect
	github.com/hashicorp/go-version v1.2.1
	github.com/libopenstorage/cloudops v0.0.0-20200604165016-9cc0977d745e
	github.com/libopenstorage/openstorage v8.0.1-0.20210421201603-7ed166ac3201+incompatible
	github.com/libopenstorage/stork v1.3.0-beta1.0.20210415204554-6b98cc805c5f // indirect
	github.com/portworx/kvdb v0.0.0-20200723230726-2734b7f40194
	github.com/portworx/px-backup-api v1.2.1-0.20210416161003-f19256c6e2c5 // indirect
	github.com/portworx/sched-ops v1.20.4-rc1.0.20210407163031-09e9dcbb0f2f
	github.com/portworx/torpedo v0.20.4-rc1.0.20210325154352-eb81b0cdd145
	github.com/pquerna/cachecontrol v0.1.0 // indirect
	github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring v0.46.0
	github.com/prometheus-operator/prometheus-operator/pkg/client v0.46.0
	github.com/sirupsen/logrus v1.7.0
	github.com/stretchr/testify v1.7.0
	github.com/urfave/cli v1.22.2
	golang.org/x/crypto v0.0.0-20210421170649-83a5a9bb288b // indirect
	golang.org/x/net v0.0.0-20210420210106-798c2154c571 // indirect
	golang.org/x/oauth2 v0.0.0-20210413134643-5e61552d6c78 // indirect
	golang.org/x/sys v0.0.0-20210420205809-ac73e9fd8988 // indirect
	google.golang.org/genproto v0.0.0-20210421164718-3947dc264843 // indirect
	google.golang.org/grpc v1.37.0
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/api v0.20.4
	k8s.io/apiextensions-apiserver v0.20.4
	k8s.io/apimachinery v0.20.4
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/code-generator v0.20.4
	k8s.io/klog v1.0.0
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
