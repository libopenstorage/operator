package types

import (
	"testing"
)

// TestCase represent one or more testrail cases
type TestCase struct {
	// TestName should be readable, could be used for regex matching
	TestName string
	// TestrailCaseIDs one or more testrail case IDs
	TestrailCaseIDs []string
	// TestSpec specifies the spec for testing, e.g. a StorageCluster
	TestSpec func(*testing.T) interface{}
	// TestFunc is the function where we pass TestSpec to
	TestFunc func(tc *TestCase) func(*testing.T)
	// instantiate the given StorageCluster spec, check that it
	// started/failed to start correctly, and then remove it.
	ShouldStartSuccessfully bool
	// ShouldSkip check env requirements for the test case
	ShouldSkip func() bool
	// PureBackendRequirements contains additional information to construct a px-pure-secret.
	PureBackendRequirements *PureBackendRequirements

	// TODO: Add TestSetup and TestTearDown phase func when we have more cases
}

// RunTest executes the actual test function
func (tc *TestCase) RunTest(t *testing.T) {
	if tc.ShouldSkip == nil {
		tc.ShouldSkip = func() bool { return false }
	}
	t.Run(tc.TestName, tc.TestFunc(tc))
}
