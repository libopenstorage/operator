package healthcheck

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (
	PortworxDefaultHintBaseUrl = "https://docs.portworx.com/codes/"
)

var (
	// ErrRetry is used when a health check needs to be retried
	ErrRetry = errors.New("waiting for check to complete")
)

type Checker struct {
	// Description is the short description that's printed to the command line
	// when the check is executed
	Description string

	// HintAnchor, when appended to `HintBaseURL`, provides a URL to more
	// information about the check
	HintAnchor string

	// Fatal indicates that all remaining checks should be aborted if this check
	// fails; it should only be used if subsequent checks cannot possibly succeed
	// (default false)
	Fatal bool

	// Warning indicates that if this check fails, it should be reported, but it
	// should not impact the overall outcome of the health check (default false)
	Warning bool

	// RetryDeadline establishes a timeout value for how long a check can be
	// retried; if the deadline has passed, the check fails (default: no retries)
	RetryDeadline time.Duration

	// SurfaceErrorOnRetry indicates that the error message should be displayed
	// even if the check will be retried.  This is useful if the error message
	// contains the current status of the check.
	SurfaceErrorOnRetry bool

	// Check is the function that's called to execute the check; if the function
	// returns an error, the check fails.
	// If the check needs to be retried, set a RetryDeadline value.
	// If the check needs to be skipped, return SkipError.
	// Checks are free to set their own context with timeouts, tokens, etc.
	Check func(context.Context, *HealthCheckState) error
}

// CheckResult encapsulates a check's identifying information and output
type CheckResult struct {
	Category    CategoryID
	Description string
	HintURL     string
	Retry       bool
	Warning     bool
	Err         error
}

type HealthChecker struct {
	categories     []*Category
	state          *HealthCheckState
	retryWindow    time.Duration
	contextTimeOut time.Duration
}

var (
	DefaultRetryWindow    = 5 * time.Second
	DefaultContextTimeOut = 30 * time.Second
)

// NewHealthChecker returns a health check manager which can be used to execute
// all the categories and their health checks.
func NewHealthChecker(categories []*Category) *HealthChecker {
	return &HealthChecker{
		categories:     categories,
		contextTimeOut: DefaultContextTimeOut,
		retryWindow:    DefaultRetryWindow,
		state: &HealthCheckState{
			data: make(map[HealthStateDataKey]any),
		},
	}
}

// WithRetryWindow sets the amount of time to wait when a failed check can be
// retried due to its RetryDeadline value
func (hc *HealthChecker) WithRetryWindow(d time.Duration) *HealthChecker {
	if hc == nil {
		return nil
	}
	hc.retryWindow = d
	return hc
}

// WithContextTimeOut sets the timeout provided to the context of each check
func (hc *HealthChecker) WithContextTimeout(d time.Duration) *HealthChecker {
	if hc == nil {
		return nil
	}
	hc.contextTimeOut = d
	return hc
}

func (hc *HealthChecker) State() *HealthCheckState {
	if hc == nil {
		return nil
	}
	return hc.state
}

// TestHintsInHealthCheckers returns error if any of the URL hints do not exist.
// Use it in your unit tests to confirm that the URL hints exist.
// See cmd/px-healthcheck/handler/start/start_test.go
func TestHintsInHealthCheckers(categories []*Category) error {
	return NewHealthChecker(categories).TestHints()
}

// AppendCategories returns a HealthChecker instance appending the provided Categories
func (hc *HealthChecker) AppendCategories(categories ...*Category) *HealthChecker {
	hc.categories = append(hc.categories, categories...)
	return hc
}

// GetCategories returns all the categories
func (hc *HealthChecker) GetCategories() []*Category {
	return hc.categories
}

// Run executes all the health checks and collects all the information. Note
// that this call does not return until all checks have completed.
// If you want to be notified for each health check completion,
// please use RunChecks()
func (hc *HealthChecker) Run() Reporter {
	reporter := NewSimpleReporter()
	reporter.success, reporter.warning = hc.RunChecks(reporter.Observer)
	return reporter
}

// RunChecks runs all configured checkers, and passes the results of each
// check to the observer. If a check fails and is marked as fatal, then all
// remaining checks are skipped. If at least one check fails, RunChecks returns
// false; if all checks passed, RunChecks returns true.  Checks which are
// designated as warnings will not cause RunCheck to return false, however.
func (hc *HealthChecker) RunChecks(observer CheckObserver) (bool, bool) {
	success := true
	warning := false
	for _, c := range hc.categories {
		if c.Enabled {
			for _, checker := range c.Checkers {
				if checker.Check != nil {
					if !hc.runCheck(c, checker, observer) {
						if !checker.Warning {
							success = false
						} else {
							warning = true
						}
						if checker.Fatal {
							return success, warning
						}
					}
				}
			}
		}
	}
	return success, warning
}

// TestHints must be used in your unit tests to check that the HintURL of
// your checks and categories exist. This function does not execute the checks.
// It only checks the URL hints.
// Example: cmd/px-healthcheck/handler/start/start_test.go
func (hc *HealthChecker) TestHints() error {
	type checkInfo struct {
		url         string
		description string
	}

	workers := 10
	checkUrlCh := make(chan *checkInfo, workers)
	errChan := make(chan error, workers)

	testHint := func() {
		for info := range checkUrlCh {
			resp, err := http.Head(info.url)
			if err != nil {
				errChan <- err
				continue
			}

			if resp.StatusCode != http.StatusOK {
				errChan <- fmt.Errorf("check [%s] has an incorrect or invalid HintAnchor. failed to get access to %s, returned HTTP code %d",
					info.description,
					info.url,
					resp.StatusCode)
				continue
			}

			errChan <- nil
		}
	}

	var savedError error
	var wg sync.WaitGroup
	errCheck := func() {
		for err := range errChan {
			if err != nil {
				savedError = err
			}
			wg.Done()
		}
	}

	for i := 0; i < workers; i++ {
		go testHint()
	}
	go errCheck()

	for _, c := range hc.categories {
		if c.Enabled {
			for _, checker := range c.Checkers {
				if checker.Check != nil {
					checkUrlCh <- &checkInfo{
						url:         getHintUrl(c.HintBaseURL, checker.HintAnchor),
						description: checker.Description,
					}
					wg.Add(1)
				}
			}
		}
	}
	wg.Wait()
	close(checkUrlCh)
	close(errChan)

	return savedError
}

func (hc *HealthChecker) runCheck(category *Category, c *Checker, observer CheckObserver) bool {
	deadline := time.Now().Add(c.RetryDeadline)
	for {
		ctx, cancel := context.WithTimeout(category.Context, hc.contextTimeOut)
		err := c.Check(ctx, hc.state)
		cancel()
		var se SkipError
		if errors.As(err, &se) {
			return true
		}

		checkResult := &CheckResult{
			Category:    category.ID,
			Description: c.Description,
			Warning:     c.Warning,
			HintURL:     getHintUrl(category.HintBaseURL, c.HintAnchor),
		}
		var vs VerboseSuccess
		if errors.As(err, &vs) {
			checkResult.Description = fmt.Sprintf("%s\n%s", checkResult.Description, vs.Message)
		} else if err != nil {
			checkResult.Err = CategoryError{category.ID, err}
		}

		if checkResult.Err != nil && time.Now().Before(deadline) {
			checkResult.Retry = true

			// Check if the error provided by the check should be provided
			// to the observer. If not, override it with a generic waiting message
			if !c.SurfaceErrorOnRetry {
				checkResult.Err = ErrRetry
			}

			observer(checkResult)
			time.Sleep(hc.retryWindow)
			continue
		}

		observer(checkResult)
		return checkResult.Err == nil
	}
}

func getHintUrl(base, anchor string) string {
	return base + anchor
}
