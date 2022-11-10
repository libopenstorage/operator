package cloudprovider

import (
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"
)

const (
	pxTopologyLabel = "topology.portworx.io"
	pxZoneLabel     = pxTopologyLabel + "/zone"
	//Deprecated: having this only to support backward compatibility
	pxZoneLabelDeprecated = "px/zone"
)

// zoneLabelsPriority is the priority of zone labels that px uses to determine the zone
var zoneLabelsPriority = [4]string{pxZoneLabel, pxZoneLabelDeprecated, v1.LabelTopologyZone, v1.LabelZoneFailureDomain}

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

// New returns a new implementation of the cloud provider
func New(name string) Ops {
	providerRegistryLock.Lock()
	defer providerRegistryLock.Unlock()

	ops, ok := providerRegistry[name]
	if !ok {
		return &defaultProvider{name}
	}
	return ops
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
	zone := "default"
	for _, label := range zoneLabelsPriority {
		if name, ok := node.Labels[label]; ok {
			zone = name
			break
		}
	}
	return zone, nil
}

func init() {
	providerRegistryLock.Lock()
	defer providerRegistryLock.Unlock()

	providerRegistry = make(map[string]Ops)
	providerRegistry[azureName] = &azure{}
}
