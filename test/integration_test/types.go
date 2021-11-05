// +build integrationtest

package integrationtest

import (
	"encoding/json"
	"testing"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
)

const (
	// PxNamespace is a default namespace for StorageCluster
	PxNamespace = "kube-system"
)

// TestCase represent one or more testrail cases
type TestCase struct {
	// TestName should be readable, could be used for regex matching
	TestName string
	// TestrailCaseIDs one or more testrail case IDs
	TestrailCaseIDs []string
	// TestSpec specifies the spec for testing, e.g. a StorageCluster
	TestSpec func(*testing.T) interface{}
	// for fafb only, will remove and use TestSpec only after review
	Spec corev1.StorageClusterSpec
	// TestFunc is the function where we pass TestSpec to
	TestFunc func(tc *TestCase) func(*testing.T)
	// instantiate the given StorageCluster spec, check that it
	// started/failed to start correctly, and then remove it.
	ShouldStartSuccessfully bool
	// ShouldSkip check env requirements for the test case
	ShouldSkip func() bool
}

// BackendRequirements is used to describe what backends are required for
// a given test, including how many should be invalid.
type BackendRequirements struct {
	// RequiredArrays and RequiredBlades indicate how many FlashArrays and
	// FlashBlades are required respectively. If not enough are present in
	// the source secret, this test will instead be skipped. If set to
	// AllAvailableBackends, all backends of that type will be used.
	RequiredArrays, RequiredBlades int
	// InvalidArrays and InvalidBlades indicate how many FlashArrays and
	// FlashBlades respectively should have invalid credentials supplied.
	// If set to AllAvailableBackends, all backends will have their
	// credentials set invalid.
	InvalidArrays, InvalidBlades int
}

// PureTestrailCase is a TestCase with additional
// information to construct a px-pure-secret.
type PureTestrailCase struct {
	TestCase
	BackendRequirements BackendRequirements
}

// DiscoveryConfig represents a single pure.json file
type DiscoveryConfig struct {
	Arrays []FlashArrayEntry `json:"FlashArrays,omitempty"`
	Blades []FlashBladeEntry `json:"FlashBlades,omitempty"`
}

// FlashArrayEntry represents a single FlashArray in a pure.json file
type FlashArrayEntry struct {
	APIToken     string            `json:"APIToken,omitempty"`
	MgmtEndPoint string            `json:"MgmtEndPoint,omitempty"`
	Labels       map[string]string `json:"Labels,omitempty"`
}

// FlashBladeEntry represents a single FlashBlade in a pure.json file
type FlashBladeEntry struct {
	MgmtEndPoint string            `json:"MgmtEndPoint,omitempty"`
	NFSEndPoint  string            `json:"NFSEndPoint,omitempty"`
	APIToken     string            `json:"APIToken,omitempty"`
	Labels       map[string]string `json:"Labels,omitempty"`
}

// DumpJSON returns this DiscoveryConfig in a JSON byte array,
// the correct format for use in the k8s Secret API
func (dc *DiscoveryConfig) DumpJSON() ([]byte, error) {
	return json.Marshal(*dc)
}

// RunTest executes the actual test function
func (tc *TestCase) RunTest(t *testing.T) {
	t.Run(tc.TestName, tc.TestFunc(tc))
}
