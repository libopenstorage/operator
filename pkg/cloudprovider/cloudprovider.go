package cloudprovider

import (
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"

	"github.com/libopenstorage/cloudops"
	"github.com/libopenstorage/operator/pkg/preflight"
)

const (
	failureDomainZoneKey   = v1.LabelTopologyZone
	failureDomainRegionKey = v1.LabelTopologyRegion
)

var (
	providerRegistry     map[string]Ops
	providerRegistryLock sync.Mutex
)

// Ops is a list of APIs to fetch information about cloudprovider and its nodes
type Ops interface {
	// Name returns the name of the cloud provider
	Name() string

	// GetZone returns the zone of the provided node
	GetZone(*v1.Node) (string, error)
}

type defaultProvider struct {
	name string
}

func (d *defaultProvider) Name() string {
	return d.name
}

func (d *defaultProvider) GetZone(node *v1.Node) (string, error) {
	if node == nil {
		return "", fmt.Errorf("node cannot be nil")
	}
	return node.Labels[failureDomainZoneKey], nil
}

// Get returns the cloud provider
func Get() Ops {
	return New(preflight.Instance().ProviderName())
}

// New returns a new implementation of the cloud provider
func New(name string) Ops {
	providerRegistryLock.Lock()
	defer providerRegistryLock.Unlock()

	ops, ok := providerRegistry[name]
	if !ok {
		return &defaultProvider{
			name: name,
		}
	}
	return ops
}

func init() {
	providerRegistryLock.Lock()
	defer providerRegistryLock.Unlock()

	providerRegistry = make(map[string]Ops)
	providerRegistry[cloudops.Azure] = &azure{}
	providerRegistry[string(cloudops.AWS)] = &aws{}
}
