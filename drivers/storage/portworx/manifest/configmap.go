package manifest

import (
	"context"

	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultConfigMapName = "px-versions"
	versionConfigMapKey  = "versions"
)

type configMap struct {
	cm *v1.ConfigMap
}

func newConfigMapManifest(
	k8sClient client.Client,
	cluster *corev1alpha1.StorageCluster,
) (manifest, error) {
	versionCM := &v1.ConfigMap{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      defaultConfigMapName,
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
	data, exists := m.cm.Data[versionConfigMapKey]
	if !exists {
		// If the exact key does not exist, just take the first one
		// as only one key is expected
		for _, value := range m.cm.Data {
			data = value
			break
		}
	}
	return parseVersionManifest([]byte(data))
}
