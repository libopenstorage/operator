module github.com/libopenstorage/operator

go 1.19

require (
	github.com/Azure/azure-sdk-for-go v68.0.0+incompatible // indirect
	github.com/Azure/go-autorest/autorest/azure/auth v0.5.5 // indirect
	github.com/Azure/go-autorest/autorest/azure/cli v0.4.2 // indirect
	github.com/aws/aws-sdk-go v1.40.39
	github.com/go-logr/logr v1.2.3
	github.com/golang-jwt/jwt/v4 v4.4.3
	github.com/golang/mock v1.6.0
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510
	github.com/google/uuid v1.3.0
	github.com/hashicorp/go-version v1.6.0
	github.com/libopenstorage/cloudops v0.0.0-20230824113500-6dce65cb204c
	github.com/libopenstorage/openstorage v9.4.47+incompatible
	github.com/openshift/api v0.0.0-20230426193520-54a14470e5dc
	github.com/operator-framework/api v0.17.1
	github.com/pborman/uuid v1.2.1
	github.com/portworx/kvdb v0.0.0-20230405233801-87666830d3fd
	github.com/portworx/sched-ops v1.20.4-rc1.0.20230802175859-ff4459c520ae
	github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring v0.63.0
	github.com/prometheus-operator/prometheus-operator/pkg/client v0.46.0
	github.com/sirupsen/logrus v1.9.0
	github.com/stretchr/testify v1.8.2
	github.com/urfave/cli v1.22.2
	golang.org/x/sys v0.6.0
	google.golang.org/grpc v1.51.0
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/api v0.26.3
	k8s.io/apiextensions-apiserver v0.26.1
	k8s.io/apimachinery v0.26.3
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/code-generator v0.25.1
	k8s.io/component-base v0.26.1
	k8s.io/component-helpers v0.25.1
	k8s.io/klog v1.0.0
	k8s.io/kube-scheduler v0.0.0
	k8s.io/kubernetes v1.25.1
	k8s.io/utils v0.0.0-20230313181309-38a27ef9d749
	sigs.k8s.io/cluster-api v0.2.11
	sigs.k8s.io/controller-runtime v0.14.5
	sigs.k8s.io/controller-tools v0.11.3
	sigs.k8s.io/yaml v1.3.0
)

require (
	github.com/Azure/go-autorest v14.2.0+incompatible // indirect
	github.com/Azure/go-autorest/autorest v0.11.28 // indirect
	github.com/Azure/go-autorest/autorest/adal v0.9.22 // indirect
	github.com/Azure/go-autorest/autorest/date v0.3.0 // indirect
	github.com/Azure/go-autorest/autorest/to v0.4.0 // indirect
	github.com/Azure/go-autorest/autorest/validation v0.3.1 // indirect
	github.com/Azure/go-autorest/logger v0.2.1 // indirect
	github.com/Azure/go-autorest/tracing v0.6.0 // indirect
	github.com/armon/go-metrics v0.3.10 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.1.2 // indirect
	github.com/codegangsta/inject v0.0.0-20150114235600-33e0aa1cb7c0 // indirect
	github.com/codeskyblue/go-sh v0.0.0-20170112005953-b097669b1569 // indirect
	github.com/coreos/go-oidc v2.2.1+incompatible // indirect
	github.com/coreos/go-semver v0.3.0 // indirect
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf // indirect
	github.com/coreos/pkg v0.0.0-20180928190104-399ea9e2e55f // indirect
	github.com/cpuguy83/go-md2man/v2 v2.0.2 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dimchansky/utfbom v1.1.0 // indirect
	github.com/docker/distribution v2.8.2+incompatible // indirect
	github.com/emicklei/go-restful/v3 v3.9.0 // indirect
	github.com/evanphx/json-patch v5.6.0+incompatible // indirect
	github.com/evanphx/json-patch/v5 v5.6.0 // indirect
	github.com/fatih/color v1.13.0 // indirect
	github.com/fsnotify/fsnotify v1.6.0 // indirect
	github.com/go-openapi/jsonpointer v0.19.5 // indirect
	github.com/go-openapi/jsonreference v0.20.0 // indirect
	github.com/go-openapi/swag v0.22.3 // indirect
	github.com/gobuffalo/flect v0.3.0 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.5.2 // indirect
	github.com/google/gnostic v0.6.9 // indirect
	github.com/google/go-cmp v0.5.9 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware v1.4.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway v1.16.0 // indirect
	github.com/hashicorp/consul/api v1.12.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-hclog v1.2.0 // indirect
	github.com/hashicorp/go-immutable-radix v1.3.1 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-rootcerts v1.0.2 // indirect
	github.com/hashicorp/golang-lru v0.5.4 // indirect
	github.com/hashicorp/serf v0.9.7 // indirect
	github.com/imdario/mergo v0.3.12 // indirect
	github.com/inconshreveable/mousetrap v1.0.1 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kr/pretty v0.3.0 // indirect
	github.com/libopenstorage/secrets v0.0.0-20210908194121-a1d19aa9713a // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.16 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.2 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/moby/spdystream v0.2.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/mohae/deepcopy v0.0.0-20170929034955-c48cc78d4826 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/pquerna/cachecontrol v0.1.0 // indirect
	github.com/prometheus/client_golang v1.14.0 // indirect
	github.com/prometheus/client_model v0.3.0 // indirect
	github.com/prometheus/common v0.37.0 // indirect
	github.com/prometheus/procfs v0.8.0 // indirect
	github.com/rogpeppe/go-internal v1.9.0 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/spf13/cobra v1.6.1 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	go.etcd.io/etcd v0.5.0-alpha.5.0.20200910180754-dd1b699fc489 // indirect
	go.uber.org/atomic v1.7.0 // indirect
	go.uber.org/multierr v1.6.0 // indirect
	go.uber.org/zap v1.24.0 // indirect
	golang.org/x/crypto v0.6.0 // indirect
	golang.org/x/mod v0.8.0 // indirect
	golang.org/x/net v0.8.0 // indirect
	golang.org/x/oauth2 v0.0.0-20221014153046-6fdb5e3db783 // indirect
	golang.org/x/term v0.6.0 // indirect
	golang.org/x/text v0.8.0 // indirect
	golang.org/x/time v0.3.0 // indirect
	golang.org/x/tools v0.6.0 // indirect
	gomodules.xyz/jsonpatch/v2 v2.2.0 // indirect
	google.golang.org/appengine v1.6.7 // indirect
	google.golang.org/genproto v0.0.0-20221205194025-8222ab48f5fc // indirect
	google.golang.org/protobuf v1.28.1 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/square/go-jose.v2 v2.5.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/apiserver v0.25.1 // indirect
	k8s.io/gengo v0.0.0-20211129171323-c02415ce4185 // indirect
	k8s.io/klog/v2 v2.90.1 // indirect
	k8s.io/kube-openapi v0.0.0-20221012153701-172d655c2280 // indirect
	sigs.k8s.io/json v0.0.0-20221116044647-bc3834ca7abd // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.2.3 // indirect
)

replace (
	github.com/kubernetes-incubator/external-storage => github.com/libopenstorage/external-storage v5.1.1-0.20190919185747-9394ee8dd536+incompatible
	github.com/libopenstorage/openstorage => github.com/libopenstorage/openstorage v1.0.1-0.20230511212757-41751b27d69f
	golang.org/x/tools => golang.org/x/tools v0.1.11
	google.golang.org/grpc => google.golang.org/grpc v1.29.1
	google.golang.org/grpc/examples/helloworld/helloworld => google.golang.org/grpc/examples/helloworld/helloworld v1.29.1
	gopkg.in/fsnotify.v1 v1.4.7 => github.com/fsnotify/fsnotify v1.4.7

	// Replacing k8s.io dependencies is required if a dependency or any dependency of a dependency
	// depends on k8s.io/kubernetes. See https://github.com/kubernetes/kubernetes/issues/79384#issuecomment-505725449
	k8s.io/api => k8s.io/api v0.25.1
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.25.1
	k8s.io/apimachinery => k8s.io/apimachinery v0.25.1
	k8s.io/apiserver => k8s.io/apiserver v0.25.1
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.25.1
	k8s.io/client-go => k8s.io/client-go v0.25.1
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.25.1
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.25.1
	k8s.io/code-generator => k8s.io/code-generator v0.25.1
	k8s.io/component-base => k8s.io/component-base v0.25.1
	k8s.io/component-helpers => k8s.io/component-helpers v0.25.1
	k8s.io/controller-manager => k8s.io/controller-manager v0.25.1
	k8s.io/cri-api => k8s.io/cri-api v0.25.1
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.25.1
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.25.1
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.25.1
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.25.1
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.25.1
	k8s.io/kubectl => k8s.io/kubectl v0.25.1
	k8s.io/kubelet => k8s.io/kubelet v0.25.1
	k8s.io/kubernetes => k8s.io/kubernetes v1.25.1
	k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.25.1
	k8s.io/metrics => k8s.io/metrics v0.25.1
	k8s.io/mount-utils => k8s.io/mount-utils v0.25.1
	k8s.io/pod-security-admission => k8s.io/pod-security-admission v0.25.1
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.25.1

	sigs.k8s.io/controller-runtime => sigs.k8s.io/controller-runtime v0.13.0
)
