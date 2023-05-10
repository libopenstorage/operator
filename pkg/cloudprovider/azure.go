package cloudprovider

import (
	"fmt"
	"strings"

	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"

	"github.com/libopenstorage/cloudops"
)

//lint:file-ignore U1000 Ignore unused code
type azure struct {
	defaultProvider
}

func (a *azure) Name() string {
	return cloudops.Azure
}

func (a *azure) GetZone(node *v1.Node) (string, error) {
	if node == nil {
		return "", fmt.Errorf("node cannot be nil")
	}
	// if region is empty we want isAvailabilityZone to be false
	region, ok := node.Labels[v1.LabelTopologyRegion]
	if !ok {
		logrus.Warnf("Failed to get azure region info for node %v", node.Name)
	}
	zone, ok := node.Labels[v1.LabelTopologyZone]
	if !ok {
		logrus.Warnf("Failed to get azure zone info for node %v", node.Name)
	}

	if a.isAvailabilityZone(zone, region) {
		return zone, nil
	}
	return "", nil
}

// isAvailabilityZone returns true if the zone is in format of <region>-<zone-id>.
// This is done to differentiate between availability sets and availability zones
func (a *azure) isAvailabilityZone(zone string, region string) bool {
	return strings.HasPrefix(zone, fmt.Sprintf("%s-", region))
}
