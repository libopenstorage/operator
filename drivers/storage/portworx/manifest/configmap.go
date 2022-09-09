package manifest

import (
	"context"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// DefaultConfigMapName is name of the version manifest configMap.
	DefaultConfigMapName = "px-versions"
	// VersionConfigMapKey is key of version manifest content in configMap.
	VersionConfigMapKey = "versions"
)

type configMap struct {
	cm *v1.ConfigMap
}

func newConfigMapManifest(
	k8sClient client.Client,
	cluster *corev1.StorageCluster,
) (versionProvider, error) {
	versionCM := &v1.ConfigMap{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      DefaultConfigMapName,
			Namespace: cluster.Namespace,
		},
		versionCM,
	)
	if err != nil {
		return nil, err
	}

	return &configMap{
		cm: versionCM.DeepCopy(),
	}, nil
}

func (m *configMap) Get() (*Version, error) {
	return ParseVersionConfigMap(m.cm)
}
