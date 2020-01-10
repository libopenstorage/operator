package component

import (
	"sync"

	"github.com/hashicorp/go-version"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	registerLock sync.Mutex
)

// PortworxComponent interface that any Portworx component should be implement.
// The operator will reconcile all the registered Portworx components.
type PortworxComponent interface {
	// Initialize initializes the component
	Initialize(
		k8sClient client.Client,
		k8sVersion version.Version,
		scheme *runtime.Scheme,
		recorder record.EventRecorder,
	)
	// IsEnabled checks if the components needs to be enabled based on the StorageCluster
	IsEnabled(cluster *corev1alpha1.StorageCluster) bool
	// Reconcile reconciles the component to match the current state of the StorageCluster
	Reconcile(cluster *corev1alpha1.StorageCluster) error
	// Delete deletes the component if present
	Delete(cluster *corev1alpha1.StorageCluster) error
	// MarkDeleted marks the component as deleted in situations like StorageCluster deletion
	MarkDeleted()
}

var (
	components = make(map[string]PortworxComponent)
)

// Register registers a PortworxComponent and stores in the global map of components
func Register(name string, c PortworxComponent) {
	logrus.Debugf("Registering component %v with Portworx driver", name)
	registerLock.Lock()
	defer registerLock.Unlock()
	components[name] = c
}

// Get returns a PortworxComponent if present else returns (nil, false)
func Get(name string) (PortworxComponent, bool) {
	c, exists := components[name]
	return c, exists
}

// GetAll returns all the Portworx components that are registered
func GetAll() map[string]PortworxComponent {
	componentsCopy := make(map[string]PortworxComponent)
	for name, comp := range components {
		componentsCopy[name] = comp
	}
	return componentsCopy
}

// DeregisterAllComponents removes all registered components from the list.
// This is used only for testing.
func DeregisterAllComponents() {
	registerLock.Lock()
	defer registerLock.Unlock()
	components = make(map[string]PortworxComponent)
}
