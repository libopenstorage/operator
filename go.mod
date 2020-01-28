module github.com/libopenstorage/operator

go 1.13

require (
	github.com/PuerkitoBio/purell v1.1.1 // indirect
	github.com/PuerkitoBio/urlesc v0.0.0-20170810143723-de5bf2ad4578
	github.com/armon/go-metrics v0.0.0-20190430140413-ec5e00d3c878
	github.com/beorn7/perks v0.0.0-20180321164747-3a771d992973
	github.com/cenkalti/backoff v2.2.1+incompatible
	github.com/coreos/etcd v3.3.13+incompatible
	github.com/coreos/go-oidc v2.1.0+incompatible
	github.com/coreos/go-semver v0.3.0
	github.com/coreos/prometheus-operator v0.31.1
	github.com/davecgh/go-spew v1.1.1
	github.com/dgrijalva/jwt-go v3.2.0+incompatible
	github.com/docker/distribution v2.7.1+incompatible
	github.com/docker/spdystream v0.0.0-20181023171402-6480d4af844c
	github.com/emicklei/go-restful v2.10.0+incompatible
	github.com/envoyproxy/go-control-plane v0.6.9 // indirect
	github.com/evanphx/json-patch v4.5.0+incompatible
	github.com/go-logr/logr v0.1.0
	github.com/go-logr/zapr v0.1.1
	github.com/go-openapi/jsonpointer v0.19.3 // indirect
	github.com/go-openapi/jsonreference v0.19.3 // indirect
	github.com/go-openapi/spec v0.19.3
	github.com/go-openapi/swag v0.19.5 // indirect
	github.com/gogo/googleapis v1.1.0 // indirect
	github.com/gogo/protobuf v1.3.1
	github.com/golang/groupcache v0.0.0-20190129154638-5b532d6fd5ef
	github.com/golang/mock v1.3.1-0.20190508161146-9fa652df1129
	github.com/golang/protobuf v1.3.2
	github.com/google/gofuzz v1.0.0
	github.com/google/shlex v0.0.0-20181106134648-c34317bd91bf
	github.com/google/uuid v1.1.1
	github.com/googleapis/gnostic v0.3.1
	github.com/grpc-ecosystem/go-grpc-middleware v1.0.0
	github.com/grpc-ecosystem/grpc-gateway v1.11.3
	github.com/hashicorp/consul v1.4.4
	github.com/hashicorp/go-cleanhttp v0.5.1
	github.com/hashicorp/go-immutable-radix v1.0.0 // indirect
	github.com/hashicorp/go-rootcerts v1.0.0
	github.com/hashicorp/go-version v1.2.0
	github.com/hashicorp/golang-lru v0.5.1
	github.com/hashicorp/serf v0.8.3
	github.com/imdario/mergo v0.3.8
	github.com/json-iterator/go v1.1.8
	github.com/konsorten/go-windows-terminal-sequences v1.0.2 // indirect
	github.com/libopenstorage/cloudops v0.0.0-20190815012442-6e0d676b6c3e
	github.com/libopenstorage/openstorage v8.0.0+incompatible
	github.com/lyft/protoc-gen-validate v0.0.13 // indirect
	github.com/mailru/easyjson v0.7.0
	github.com/matttproud/golang_protobuf_extensions v1.0.1
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/mitchellh/mapstructure v1.1.2
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd
	github.com/modern-go/reflect2 v1.0.1
	github.com/mohae/deepcopy v0.0.0-20170929034955-c48cc78d4826
	github.com/opencontainers/go-digest v1.0.0-rc1
	github.com/operator-framework/operator-sdk v0.10.0
	github.com/pborman/uuid v1.2.0
	github.com/pmezard/go-difflib v1.0.0
	github.com/portworx/kvdb v0.0.0-20181128025101-d281ecddf165
	github.com/portworx/sched-ops v0.0.0-20200318041339-a47eff4118aa
	github.com/pquerna/cachecontrol v0.0.0-20180517163645-1555304b9b35
	github.com/prometheus/client_golang v0.9.3-0.20190127221311-3c4408c8b829
	github.com/prometheus/client_model v0.0.0-20190129233127-fd36f4220a90
	github.com/prometheus/common v0.4.0
	github.com/prometheus/procfs v0.0.0-20190403104016-ea9eea638872
	github.com/sirupsen/logrus v1.4.2
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.4.0
	github.com/urfave/cli v1.20.0
	go.uber.org/atomic v1.3.2
	go.uber.org/multierr v1.1.0
	go.uber.org/zap v1.9.1
	golang.org/x/crypto v0.0.0-20191010185427-af544f31c8ac
	golang.org/x/net v0.0.0-20191009170851-d66e71096ffb
	golang.org/x/oauth2 v0.0.0-20190604053449-0f29369cfe45
	golang.org/x/sys v0.0.0-20191010194322-b09406accb47
	golang.org/x/text v0.3.2
	golang.org/x/time v0.0.0-20190921001708-c4c64cad1fd0
	golang.org/x/tools v0.0.0-20190920225731-5eefd052ad72
	gomodules.xyz/jsonpatch v2.0.1+incompatible // indirect
	gomodules.xyz/jsonpatch/v2 v2.0.1
	gonum.org/v1/gonum v0.7.0
	google.golang.org/appengine v1.6.5
	google.golang.org/genproto v0.0.0-20191009194640-548a555dbc03
	google.golang.org/grpc v1.24.0
	gopkg.in/fsnotify.v1 v1.4.7
	gopkg.in/inf.v0 v0.9.1
	gopkg.in/square/go-jose.v2 v2.3.1
	gopkg.in/yaml.v2 v2.2.8
	k8s.io/api v0.0.0-20190831074750-7364b6bdad65
	k8s.io/apiextensions-apiserver v0.0.0-20190918224502-6154570c2037
	k8s.io/apimachinery v0.0.0-20190831074630-461753078381
	k8s.io/apiserver v0.0.0-20190820063401-c43cd040845a
	k8s.io/client-go v11.0.1-0.20190820062731-7e43eff7c80a+incompatible
	k8s.io/cloud-provider v0.0.0-20190831081049-76be6e1a666d
	k8s.io/code-generator v0.18.0
	k8s.io/csi-translation-lib v0.0.0-20190913091657-9745ba0e69cf
	k8s.io/gengo v0.0.0-20200114144118-36b2048a9120
	k8s.io/klog v1.0.0
	k8s.io/kube-openapi v0.0.0-20200121204235-bf4fb3bd569c
	k8s.io/kubernetes v1.14.6
	k8s.io/utils v0.0.0-20190923111123-69764acb6e8e
	sigs.k8s.io/controller-runtime v0.2.2
	sigs.k8s.io/yaml v1.2.0
)

replace (
	github.com/coreos/etcd => github.com/coreos/etcd v3.3.13+incompatible
	github.com/kubernetes-incubator/external-storage => github.com/libopenstorage/external-storage v5.1.1-0.20190919185747-9394ee8dd536+incompatible

	github.com/kubernetes-incubator/external-storage v0.0.0-00010101000000-000000000000 => github.com/libopenstorage/external-storage v5.1.1-0.20190919185747-9394ee8dd536+incompatible
	github.com/libopenstorage/openstorage => github.com/libopenstorage/openstorage v7.0.1-0.20190815174745-65020b2cbe10+incompatible
	//github.com/portworx/sched-ops => github.com/portworx/sched-ops v0.0.0-20200114220131-81a74c790db9
	github.com/prometheus/prometheus => github.com/prometheus/prometheus v1.8.2-0.20190424153033-d3245f150225
	k8s.io/api => k8s.io/api v0.0.0-20190816222004-e3a6b8045b0b
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.0.0-20190918224502-6154570c2037
	k8s.io/apimachinery => k8s.io/apimachinery v0.0.0-20190816221834-a9f1d8a9c101
	k8s.io/apiserver => k8s.io/apiserver v0.0.0-20190820063401-c43cd040845a
	k8s.io/client-go v2.0.0-alpha.0.0.20181121191925-a47917edff34+incompatible => k8s.io/client-go v2.0.0+incompatible
	k8s.io/client-go v2.0.0-alpha.0.0.20190313235726-6ee68ca5fd83+incompatible => k8s.io/client-go v2.0.0+incompatible
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.0.0-20191004111010-9775d7be8494
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.0.0-20190913091657-9745ba0e69cf
)
