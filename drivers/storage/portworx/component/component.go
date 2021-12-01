package component

import (
	"sort"
	"sync"

	"github.com/hashicorp/go-version"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// DefaultComponentPriority default priority for Portworx components
	DefaultComponentPriority = int32(100)
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
	// Name returns the name of the component
	Name() string
	// Priority returns the priority of the component.
	// Smaller the number, higher the priority.
	Priority() int32
	// IsPausedForMigration checks if the component is paused for migration. This ensures that
	// the operator components do not interfere with the ongoing daemonset to operator migration.
	IsPausedForMigration(cluster *corev1.StorageCluster) bool
	// IsEnabled checks if the components needs to be enabled based on the StorageCluster
	IsEnabled(cluster *corev1.StorageCluster) bool
	// Reconcile reconciles the component to match the current state of the StorageCluster
	Reconcile(cluster *corev1.StorageCluster) error
	// Delete deletes the component if present
	Delete(cluster *corev1.StorageCluster) error
	// MarkDeleted marks the component as deleted for testing purposes
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
func GetAll() []PortworxComponent {
	componentsCopy := make([]PortworxComponent, 0)
	for _, comp := range components {
		componentsCopy = append(componentsCopy, comp)
	}
	sort.Sort(byPriority(componentsCopy))
	return componentsCopy
}

// DeregisterAllComponents removes all registered components from the list.
// This is used only for testing.
func DeregisterAllComponents() {
	registerLock.Lock()
	defer registerLock.Unlock()
	components = make(map[string]PortworxComponent)
}

// byPriority data interface to sort Portworx components by their priority
type byPriority []PortworxComponent

func (e byPriority) Len() int      { return len(e) }
func (e byPriority) Swap(i, j int) { e[i], e[j] = e[j], e[i] }
func (e byPriority) Less(i, j int) bool {
	return e[i].Priority() < e[j].Priority()
}
