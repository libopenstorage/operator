package cloudprovider

import (
	"context"
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/libopenstorage/cloudops"
	"github.com/libopenstorage/operator/pkg/preflight"
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

// Get returns the cloud provider
func Get() Ops {
	return New(preflight.Instance().ProviderName())
}

// GetZoneMap returns zone map
func GetZoneMap(k8sClient client.Client, filterLabelKey string, filterLabelValue string) (map[string]uint64, error) {
	zoneMap := make(map[string]uint64)
	nodeList := &v1.NodeList{}
	err := k8sClient.List(context.TODO(), nodeList, &client.ListOptions{})
	if err != nil {
		return zoneMap, err
	}
	for _, node := range nodeList.Items {
		if zone, err := Get().GetZone(&node); err == nil {
			if len(filterLabelKey) > 0 {
				value, ok := node.Labels[filterLabelKey]
				// If provided filterLabelKey is not found or the value does not match with
				// the provided filterLabelValue, no need to count in the zoneMap
				if !ok || value != filterLabelValue {
					if _, ok := zoneMap[zone]; !ok {
						zoneMap[zone] = 0
					}
					continue
				}
				// provided filterLabel's value and key matched, let's count them in the zoneMap
			}
			instancesCount := zoneMap[zone]
			zoneMap[zone] = instancesCount + 1
		} else {
			logrus.Errorf("count not find zone information: %v", err)
			return nil, err
		}
	}
	return zoneMap, nil
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
