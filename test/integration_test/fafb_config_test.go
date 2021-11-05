// +build fafb

package integrationtest

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/api/errors"

	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

const (
	// SourceConfigSecretName is the name of the secret that contains the superset of all credentials
	// we may select from for these tests.
	SourceConfigSecretName = "px-pure-secret-source"
	// OutputSecretName is the name of the secret we will output chosen credential subsets to.
	OutputSecretName = "px-pure-secret"
)

var (
	sourceConfig          DiscoveryConfig
	sourceConfigPresent   bool
	sourceConfigLoadError error
	sourceConfigLoadOnce  sync.Once
)

// AllAvailableBackends can be used in a BackendRequirements struct
// to indicate that all available FlashArrays or FlashBlades should be used
const AllAvailableBackends = -1

func loadSourceConfig(namespace string) func() {
	return func() {
		sourceConfigPresent = false

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
		sourceConfigPresent = true
		pureJSON, ok := secret.Data["pure.json"]
		if !ok {
			sourceConfigLoadError = fmt.Errorf("secret %s is missing key pure.json", SourceConfigSecretName)
			return
		}

		err = json.Unmarshal(pureJSON, &sourceConfig)
		if err != nil {
			sourceConfigLoadError = fmt.Errorf("failed to parse secret %s: %v", SourceConfigSecretName, err)
		}
	}
}

func loadSourceConfigOrFail(t *testing.T, namespace string) {
	sourceConfigLoadOnce.Do(loadSourceConfig(namespace))

	require.NoError(t, sourceConfigLoadError, "Failed to load source configuration for backend credentials (other than not found error)")
	if !sourceConfigPresent {
		t.Skip("Source config not present, skipping")
	}
}

// GenerateFleetOrSkip will attempt to create a fleet of devices matching the given
// requirements, consisting of devices from the source config.
// If not enough devices exist to meet the requirements or the source secret does not
// exist, the test will be skipped.
func GenerateFleetOrSkip(t *testing.T, namespace string,
	req BackendRequirements) DiscoveryConfig {
	loadSourceConfigOrFail(t, namespace)

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

	// Check that we have enough devices to meet the requirements
	if req.RequiredArrays > 0 && len(sourceConfig.Arrays) < req.RequiredArrays {
		t.Skipf("Test requires %d FlashArrays but only %d provided, skipping", req.RequiredArrays, len(sourceConfig.Arrays))
	}
	if req.RequiredBlades > 0 && len(sourceConfig.Blades) < req.RequiredBlades {
		t.Skipf("Test requires %d FlashBlades but only %d provided, skipping", req.RequiredBlades, len(sourceConfig.Blades))
	}

	// We have enough devices for this test, let's make a fleet and give it back for testing
	newFleet := DiscoveryConfig{}

	addedArrays := 0
	for _, value := range sourceConfig.Arrays {
		// If we have enough arrays, stop now
		if req.RequiredArrays != AllAvailableBackends && addedArrays >= req.RequiredArrays {
			break
		}

		// Copy the value over
		entry := FlashArrayEntry{
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
	for _, value := range sourceConfig.Blades {
		// If we have enough blades, stop now
		if req.RequiredBlades != AllAvailableBackends && addedBlades >= req.RequiredBlades {
			break
		}

		// Copy the value over
		entry := FlashBladeEntry{
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
