package component

import (
	"fmt"
	"time"

	"github.com/hashicorp/go-version"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	"github.com/sirupsen/logrus"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// PortworxCRDComponentName name of the Portworx CRDs component
	PortworxCRDComponentName = "Portworx CRDs"
)

type portworxCRD struct {
	isVolumePlacementStrategyCRDCreated bool
}

func (c *portworxCRD) Initialize(
	_ client.Client,
	_ version.Version,
	_ *runtime.Scheme,
	_ record.EventRecorder,
) {
	// k8sClient is not needed as we use k8s.Instance for CRDs
}

func (c *portworxCRD) IsEnabled(cluster *corev1.StorageCluster) bool {
	return pxutil.IsPortworxEnabled(cluster)
}

func (c *portworxCRD) Reconcile(cluster *corev1.StorageCluster) error {
	if !c.isVolumePlacementStrategyCRDCreated {
		if err := createVolumePlacementStrategyCRD(); err != nil {
			return NewError(ErrCritical, err)
		}
		c.isVolumePlacementStrategyCRDCreated = true
	}
	return nil
}

func (c *portworxCRD) Delete(cluster *corev1.StorageCluster) error {
	c.MarkDeleted()
	return nil
}

func (c *portworxCRD) MarkDeleted() {
	c.isVolumePlacementStrategyCRDCreated = false
}

func createVolumePlacementStrategyCRD() error {
	logrus.Debugf("Creating VolumePlacementStrategy CRD")

	resource := apiextensionsops.CustomResource{
		Plural: "volumeplacementstrategies",
		Group:  "portworx.io",
	}

	crd := &apiextensionsv1beta1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.%s", resource.Plural, resource.Group),
		},
		Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
			Group: resource.Group,
			Versions: []apiextensionsv1beta1.CustomResourceDefinitionVersion{
				{
					Name:    "v1beta2",
					Served:  true,
					Storage: true,
				},
				{
					Name:    "v1beta1",
					Served:  false,
					Storage: false,
				},
			},
			Scope: apiextensionsv1beta1.ClusterScoped,
			Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
				Singular:   "volumeplacementstrategy",
				Plural:     resource.Plural,
				Kind:       "VolumePlacementStrategy",
				ShortNames: []string{"vps", "vp"},
			},
		},
	}

	err := apiextensionsops.Instance().RegisterCRD(crd)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	return apiextensionsops.Instance().ValidateCRD(resource, 1*time.Minute, 5*time.Second)
}

// RegisterPortworxCRDComponent registers the Portworx CRD component
func RegisterPortworxCRDComponent() {
	Register(PortworxCRDComponentName, &portworxCRD{})
}

func init() {
	RegisterPortworxCRDComponent()
}
