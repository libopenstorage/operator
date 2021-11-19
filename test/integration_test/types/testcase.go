package types

import (
	"fmt"
	"strings"
	"testing"
)

var testReporter *TestReporter

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
	ShouldSkip func(tc *TestCase) bool
	// PureBackendRequirements contains additional information to construct a px-pure-secret.
	PureBackendRequirements *PureBackendRequirements

	// TODO: Add TestSetup and TestTearDown phase func when we have more cases
}

// TestReporter prints test results at the end of test runs
// TODO: integration with Testrail in the future
type TestReporter struct {
	PassCases []TestCase
	SkipCases []TestCase
	FailCases []TestCase
}

// RunTest executes the actual test function
func (tc *TestCase) RunTest(t *testing.T) {
	if tc.ShouldSkip == nil {
		tc.ShouldSkip = func(tc *TestCase) bool { return false }
	}
	if tc.ShouldSkip(tc) {
		TestReporterInstance().AddSkipCase(*tc)
		fmt.Printf("--- SKIP: %s %s\n", tc.TestName, tc.TestrailCaseIDs)
		return
	}

	// NOTE: Tests filtered out by name regex will be marked as passed as well
	passed := t.Run(tc.TestName, tc.TestFunc(tc))
	if passed {
		TestReporterInstance().AddPassCase(*tc)
	} else {
		TestReporterInstance().AddFailCase(*tc)
	}
}

// TestReporterInstance returns the test reporter instance
func TestReporterInstance() *TestReporter {
	if testReporter == nil {
		testReporter = &TestReporter{}
	}
	return testReporter
}

// AddPassCase adds passed test case to test reporter
func (tp *TestReporter) AddPassCase(tc TestCase) {
	tp.PassCases = append(tp.PassCases, tc)
}

// AddSkipCase adds skipped test case to test reporter
func (tp *TestReporter) AddSkipCase(tc TestCase) {
	tp.SkipCases = append(tp.SkipCases, tc)
}

// AddFailCase adds failed test case to test reporter
func (tp *TestReporter) AddFailCase(tc TestCase) {
	tp.FailCases = append(tp.FailCases, tc)
}

// PrintTestResult prints the test result at the end of test
func (tp *TestReporter) PrintTestResult() {
	fmt.Println(strings.Repeat("=", 50))
	defer fmt.Println(strings.Repeat("=", 50))

	// Print Passed cases
	fmt.Println("--- PASS:")
	for _, tc := range TestReporterInstance().PassCases {
		fmt.Printf("    %s %s\n", tc.TestName, tc.TestrailCaseIDs)
	}

	// Print Skipped cases
	fmt.Println("--- SKIP:")
	for _, tc := range TestReporterInstance().SkipCases {
		fmt.Printf("    %s %s\n", tc.TestName, tc.TestrailCaseIDs)
	}

	// Print Failed cases
	fmt.Println("--- FAIL:")
	for _, tc := range TestReporterInstance().FailCases {
		fmt.Printf("    %s %s\n", tc.TestName, tc.TestrailCaseIDs)
	}
}
