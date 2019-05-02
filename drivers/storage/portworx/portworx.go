package portworx

import (
	storage "github.com/libopenstorage/operator/drivers/storage"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
)

const (
	// driverName is the name of the portworx storage driver implementation
	driverName             = "portworx"
	labelKeyName           = "name"
	defaultStartPort       = 9001
	defaultSecretsProvider = "k8s"
)

type portworx struct{}

func (p *portworx) String() string {
	return driverName
}

func (p *portworx) Init(_ interface{}) error {
	return nil
}

func (p *portworx) GetSelectorLabels() map[string]string {
	return map[string]string{
		labelKeyName: driverName,
	}
}

func (p *portworx) SetDefaultsOnStorageCluster(toUpdate *corev1alpha1.StorageCluster) {
	startPort := uint32(defaultStartPort)
	if toUpdate.Spec.Kvdb == nil || len(toUpdate.Spec.Kvdb.Endpoints) == 0 {
		toUpdate.Spec.Kvdb = &corev1alpha1.KvdbSpec{
			Internal: true,
		}
	}
	if toUpdate.Spec.SecretsProvider == nil {
		toUpdate.Spec.SecretsProvider = stringPtr(defaultSecretsProvider)
	}
	if toUpdate.Spec.StartPort == nil {
		toUpdate.Spec.StartPort = &startPort
	}
	if toUpdate.Spec.Placement == nil || toUpdate.Spec.Placement.NodeAffinity == nil {
		t, err := newTemplate(toUpdate)
		if err != nil {
			return
		}
		toUpdate.Spec.Placement = &corev1alpha1.PlacementSpec{
			NodeAffinity: &v1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
					NodeSelectorTerms: []v1.NodeSelectorTerm{
						{
							MatchExpressions: t.getSelectorRequirements(),
						},
					},
				},
			},
		}
	}
}

func init() {
	if err := storage.Register(driverName, &portworx{}); err != nil {
		logrus.Panicf("Error registering portworx storage driver: %v", err)
	}
}
