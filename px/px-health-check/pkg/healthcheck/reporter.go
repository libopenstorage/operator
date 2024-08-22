package healthcheck

import (
	"encoding/json"
	"fmt"
	"io"

	common "github.com/portworx/portworxapis/sdk/golang/public/portworx/common/apiv1"
)

// Reporter provides an interface for ways to interpret and manage
// the results from the health check
type Reporter interface {
	// HasWarning returns true if the results of the health check have a warning
	HasWarning() bool
	// Successful return true if there were no errors, but it may contain warnings
	Successful() bool
	// ToJSON returns the results of the health check in JSON
	ToJSON() ([]byte, error)
	// ToHealthCheckResults returns results in protobuf format
	ToHealthCheckResults() *common.HealthCheckResults
	// Print outputs the results of the health check to an output stream
	Print(w io.Writer)
	// GetResults returns the full results of the health checks
	GetResults() []*CheckResult
	// Replay calls an observer for each health check result
	Replay(observer CheckObserver) (bool, bool)
}

var _ Reporter = &SimpleReporter{}

// SimpleReporter contains a slice of CheckResult structs.
type SimpleReporter struct {
	results  []*CheckResult
	observer CheckObserver
	success  bool
	warning  bool
}

// NewSimpleReporter provides a simple method of collecting all
// the results from a health check run.
//
// This is used by the HelthChecker.Run() receiver to collect all
// the results.
func NewSimpleReporter() *SimpleReporter {
	return NewSimpleReporterWithObserver(nil)
}

// NewSimpleReporterWithObserver not only saves the results from all
// the health checks but also calls another observer for every result.
// This can be beneficial, for example, if printing to a screen and providing
// feedback to the user on the progress of the tests as they are happenning.
func NewSimpleReporterWithObserver(observer CheckObserver) *SimpleReporter {
	return &SimpleReporter{
		results:  make([]*CheckResult, 0),
		observer: observer,
	}
}

// Observer can be used to capture the results from a RunChecks() call
func (cr *SimpleReporter) Observer(r *CheckResult) {
	cr.results = append(cr.results, r)
	if cr.observer != nil {
		cr.observer(r)
	}
}

// HasWarning returns true if run of the health checks found one with a warning
func (cr *SimpleReporter) HasWarning() bool {
	return cr.warning
}

// Successful returns true if none of the health checks failed
func (cr *SimpleReporter) Successful() bool {
	return cr.success
}

// ToJSON returns the results of the health checks in JSON
func (cr *SimpleReporter) ToJSON() ([]byte, error) {
	return json.Marshal(cr.ToHealthCheckResults())
}

// ToHealthCheckResults returns the results of the health checks in protobuf format as
// defined in the Portworx Public APIs
func (cr *SimpleReporter) ToHealthCheckResults() *common.HealthCheckResults {
	var categories []*common.HealthCheckCategory

	collectOutput := func(result *CheckResult) {
		if categories == nil || categories[len(categories)-1].Name != string(result.Category) {
			categories = append(categories, &common.HealthCheckCategory{
				Name:   string(result.Category),
				Checks: []*common.HealthCheck{},
			})
		}

		if !result.Retry {
			currentCategory := categories[len(categories)-1]
			// ignore checks that are going to be retried, we want only final results
			var status common.HealthCheckResultTypes_Type
			if result.Err == nil {
				status = common.HealthCheckResultTypes_SUCCESS
			} else if result.Warning {
				status = common.HealthCheckResultTypes_WARNING
			} else {
				status = common.HealthCheckResultTypes_ERROR
			}

			currentCheck := &common.HealthCheck{
				Description: result.Description,
				Result:      status,
			}

			if result.Err != nil {
				currentCheck.Error = result.Err.Error()

				if result.HintURL != "" {
					currentCheck.Hint = result.HintURL
				}
			}
			currentCategory.Checks = append(currentCategory.Checks, currentCheck)
		}
	}

	success, warning := cr.Replay(collectOutput)

	return &common.HealthCheckResults{
		Success:    success,
		Warning:    warning,
		Categories: categories,
	}
}

// Print sens formatted strings to an output writer
func (cr *SimpleReporter) Print(w io.Writer) {

	printer := NewObserverPrinter(w)
	success, warning := cr.Replay(printer)

	if success && !warning {
		fmt.Fprintf(w, "\nOk\n")
	} else if success && warning {
		fmt.Fprintf(w, "\nWarning\n")
	} else {
		fmt.Fprintf(w, "\nError\n")
	}
}

// GetResults returns all the health check results
func (cr *SimpleReporter) GetResults() []*CheckResult {
	return cr.results
}

// Replay calls an appropriate observer for each health check result
func (cr *SimpleReporter) Replay(observer CheckObserver) (bool, bool) {
	success := true
	warning := false
	for _, result := range cr.results {
		if result.Err != nil {
			if !result.Warning {
				success = false
			} else {
				warning = true
			}
		}
		observer(result)
	}
	return success, warning
}
