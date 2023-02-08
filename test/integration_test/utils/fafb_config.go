package utils

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/errors"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/libopenstorage/operator/test/integration_test/types"
	coreops "github.com/portworx/sched-ops/k8s/core"
)

var (
	// FAFBSourceConfig contains credentials provided in pure secret
	FAFBSourceConfig types.DiscoveryConfig
	// SourceConfigPresent checks whether pure secret present
	SourceConfigPresent   bool
	sourceConfigLoadError error
	sourceConfigLoadOnce  sync.Once
)

// AllAvailableBackends can be used in a PureBackendRequirements struct
// to indicate that all available FlashArrays or FlashBlades should be used
const AllAvailableBackends = -1

func loadSourceConfig(namespace string) func() {
	return func() {
		SourceConfigPresent = false

		secret, err := coreops.Instance().GetSecret(SourceConfigSecretName, namespace)
		if err != nil {
			if errors.IsNotFound(err) {
				// Not present, but no error either
				return
			}

			// Store error and return
			sourceConfigLoadError = err
			return
		}

		// At this point we know the secret is present, any errors will be parsing errors
		SourceConfigPresent = true
		pureJSON, ok := secret.Data["pure.json"]
		if !ok {
			sourceConfigLoadError = fmt.Errorf("secret %s is missing key pure.json", SourceConfigSecretName)
			return
		}

		err = json.Unmarshal(pureJSON, &FAFBSourceConfig)
		if err != nil {
			sourceConfigLoadError = fmt.Errorf("failed to parse secret %s: %v", SourceConfigSecretName, err)
		}
	}
}

// LoadSourceConfigOrFail loads the source config once during the test
func LoadSourceConfigOrFail(t *testing.T, namespace string) {
	sourceConfigLoadOnce.Do(loadSourceConfig(namespace))
	require.NoError(t, sourceConfigLoadError, "Failed to load source configuration for backend credentials (other than not found error)")
}

// GenerateFleet will attempt to create a fleet of devices matching the given
// requirements, consisting of devices from the source config.
func GenerateFleet(t *testing.T, namespace string,
	req *types.PureBackendRequirements) types.DiscoveryConfig {
	logrus.WithFields(logrus.Fields{
		"RequiredArrays": req.RequiredArrays,
		"RequiredBlades": req.RequiredBlades,
		"InvalidArrays":  req.InvalidArrays,
		"InvalidBlades":  req.InvalidBlades,
	}).Info("Backend requirements for FA/FB test")

	// Check that we don't require more invalid backends than required ones
	if req.RequiredArrays != AllAvailableBackends {
		require.LessOrEqual(t, req.InvalidArrays, req.RequiredArrays)
	}
	if req.RequiredBlades != AllAvailableBackends {
		require.LessOrEqual(t, req.InvalidBlades, req.RequiredBlades)
	}

	// We have enough devices for this test, let's make a fleet and give it back for testing
	newFleet := types.DiscoveryConfig{}

	addedArrays := 0
	for _, value := range FAFBSourceConfig.Arrays {
		// If we have enough arrays, stop now
		if req.RequiredArrays != AllAvailableBackends && addedArrays >= req.RequiredArrays {
			break
		}

		// Copy the value over
		entry := types.FlashArrayEntry{
			APIToken:     value.APIToken,
			MgmtEndPoint: value.MgmtEndPoint,
			Labels:       value.Labels,
		}
		// Invalid backends are in effectively random order because golang map order is not guaranteed
		if req.InvalidArrays == AllAvailableBackends || addedArrays < req.InvalidArrays {
			entry.APIToken = "invalid"
		}
		newFleet.Arrays = append(newFleet.Arrays, entry)
		addedArrays++
	}

	addedBlades := 0
	for _, value := range FAFBSourceConfig.Blades {
		// If we have enough blades, stop now
		if req.RequiredBlades != AllAvailableBackends && addedBlades >= req.RequiredBlades {
			break
		}

		// Copy the value over
		entry := types.FlashBladeEntry{
			APIToken:     value.APIToken,
			MgmtEndPoint: value.MgmtEndPoint,
			NFSEndPoint:  value.NFSEndPoint,
			Labels:       value.Labels,
		}
		// Invalid backends are in effectively random order because golang map order is not guaranteed
		if req.InvalidBlades == AllAvailableBackends || addedBlades < req.InvalidBlades {
			entry.APIToken = "invalid"
		}
		newFleet.Blades = append(newFleet.Blades, entry)
		addedBlades++
	}

	return newFleet
}

// PopulateStorageCluster populate the cluster name and update node names
func PopulateStorageCluster(tc *types.TestCase, cluster *corev1.StorageCluster) error {
	cluster.Name = MakeDNS1123Compatible(strings.Join(tc.TestrailCaseIDs, "-"))

	nodes, err := testutil.GetExpectedPxNodeList(cluster)
	if err != nil {
		return err
	}
	names := testutil.ConvertNodeListToNodeNameList(nodes)

	// Sort for consistent order between multiple tests
	sort.Strings(names)

	// For each node, if the selector looks like "replaceWithNodeNumberN", replace it with
	// the name of the Nth eligible Portworx node
	for i := range cluster.Spec.Nodes {
		if !strings.HasPrefix(cluster.Spec.Nodes[i].Selector.NodeName, NodeReplacePrefix) {
			continue
		}

		num := strings.TrimPrefix(cluster.Spec.Nodes[i].Selector.NodeName, NodeReplacePrefix)
		parsedNum, err := strconv.Atoi(num)
		if err != nil {
			return err
		}

		if parsedNum >= len(names) {
			return fmt.Errorf("requested node index %d is larger than eligible worker node count %d", parsedNum, len(names))
		}

		cluster.Spec.Nodes[i].Selector.NodeName = names[parsedNum]
	}

	return nil
}
