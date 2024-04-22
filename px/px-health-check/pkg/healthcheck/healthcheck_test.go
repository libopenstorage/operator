package healthcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	common "github.com/portworx/portworxapis/sdk/golang/public/portworx/common/apiv1"
	"github.com/stretchr/testify/assert"
)

func TestNewHealthChecker(t *testing.T) {
	hc := NewHealthChecker(nil)
	assert.NotNil(t, hc)
	assert.Len(t, hc.categories, 0)
	assert.NotNil(t, hc.state)
	assert.Len(t, hc.state.data, 0)

	assert.Equal(t, DefaultContextTimeOut, hc.contextTimeOut)
	hc.WithContextTimeout(time.Second * 123)
	assert.Equal(t, time.Second*123, hc.contextTimeOut)

	assert.Equal(t, DefaultRetryWindow, hc.retryWindow)
	hc.WithRetryWindow(time.Second * 456)
	assert.Equal(t, time.Second*456, hc.retryWindow)
}

func TestStateSetDelete(t *testing.T) {
	s := &HealthCheckState{
		data: make(map[HealthStateDataKey]any),
	}

	_, ok := s.data["test"].(int)
	assert.False(t, ok)

	x := 2
	s.Set("test", x)

	_, ok = s.data["test"].(int)
	assert.True(t, ok)

	// should panic
	assert.Panics(t, func() {
		s.Set("test", x)
	})

	s.Delete("test")
	_, ok = s.data["test"].(int)
	assert.False(t, ok)

	// should not panic
	assert.NotPanics(t, func() {
		s.Set("test", x)
	})
	_, ok = s.data["test"].(int)
	assert.True(t, ok)
}

func TestHealthCheckCategories(t *testing.T) {

	// Set up
	checkone := []*Checker{
		{
			Description: "Checker 1",
			HintAnchor:  "check1",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return nil
			},
		},
	}

	checktwo := []*Checker{
		{
			Description: "Checker 2",
			HintAnchor:  "check2",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return nil
			},
		},
	}

	cat1 := NewCategory("cat1", checkone, true, "http://test.com/")
	assert.NotNil(t, cat1)
	cat2 := NewCategory("cat2", checktwo, true, "http://test.com/")
	assert.NotNil(t, cat2)

	hc := NewHealthChecker([]*Category{cat1})
	assert.NotNil(t, hc)
	hc.AppendCategories(cat2)

	cats := hc.GetCategories()
	assert.Len(t, cats, 2)

	tr := NewSimpleReporter()
	hc.RunChecks(tr.Observer)

	assert.Len(t, tr.results, 2)
	result := tr.results[0]
	assert.Equal(t, result.Category, CategoryID("cat1"))
	assert.Equal(t, result.Description, "Checker 1")
	assert.Equal(t, result.HintURL, "http://test.com/check1")
	assert.False(t, result.Retry)
	assert.False(t, result.Warning)
	assert.NoError(t, result.Err)

	result = tr.results[1]
	assert.Equal(t, result.Category, CategoryID("cat2"))
	assert.Equal(t, result.Description, "Checker 2")
	assert.Equal(t, result.HintURL, "http://test.com/check2")
	assert.False(t, result.Retry)
	assert.False(t, result.Warning)
	assert.NoError(t, result.Err)
}

func TestSingleChecker(t *testing.T) {

	// Set up
	called := false
	checkers := []*Checker{
		{
			Description: "Checker 1",
			HintAnchor:  "check123",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				called = true
				return nil
			},
		},
	}

	cat := NewCategory("test", checkers, true, "http://test.com/")
	assert.NotNil(t, cat)
	hc := NewHealthChecker([]*Category{cat})
	assert.NotNil(t, hc)

	observer_called := false
	observer := func(r *CheckResult) {
		observer_called = true
	}

	// Test
	assert.False(t, called)
	assert.False(t, observer_called)

	tr := NewSimpleReporterWithObserver(observer)
	hc.RunChecks(tr.Observer)

	assert.Len(t, tr.results, 1)
	result := tr.results[0]
	assert.True(t, called)
	assert.True(t, observer_called)
	assert.Equal(t, result.Category, CategoryID("test"))
	assert.Equal(t, result.Description, "Checker 1")
	assert.Equal(t, result.HintURL, "http://test.com/check123")
	assert.False(t, result.Retry)
	assert.False(t, result.Warning)
	assert.NoError(t, result.Err)
}

func TestPassingDataFromCheckToCheck(t *testing.T) {

	// Set up
	checkers := []*Checker{
		{
			Description: "Checker 123",
			HintAnchor:  "check123",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				state.Set(HealthStateDataKey("one"), "two")
				return nil
			},
		},
		{
			Description: "Checker 234",
			HintAnchor:  "check234",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				if val, ok := state.Get("one").(string); !ok {
					return fmt.Errorf("Not found or not a string")
				} else {
					if val != "two" {
						return fmt.Errorf("Not equal")
					}
				}
				return nil
			},
		},
	}

	cat := NewCategory("test", checkers, true, "http://test.com/")
	assert.NotNil(t, cat)
	hc := NewHealthChecker([]*Category{cat})
	assert.NotNil(t, hc)

	// Test
	results := hc.Run().GetResults()

	assert.Len(t, results, 2)

	result := results[0]
	assert.Equal(t, result.Category, CategoryID("test"))
	assert.Equal(t, result.Description, "Checker 123")
	assert.Equal(t, result.HintURL, "http://test.com/check123")
	assert.False(t, result.Retry)
	assert.False(t, result.Warning)
	assert.NoError(t, result.Err)

	result = results[1]
	assert.Equal(t, result.Category, CategoryID("test"))
	assert.Equal(t, result.Description, "Checker 234")
	assert.Equal(t, result.HintURL, "http://test.com/check234")
	assert.False(t, result.Retry)
	assert.False(t, result.Warning)
}

func TestHealthCheckerWarning(t *testing.T) {

	called := false
	// Set up
	checkers := []*Checker{
		{
			Description: "Checker 123",
			HintAnchor:  "check123",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return fmt.Errorf("ERROR")
			},
			Warning: true,
		},
		{
			Description: "Checker 234",
			HintAnchor:  "check234",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				called = true
				return nil
			},
		},
	}

	cat := NewCategory("test", checkers, true, "http://test.com/")
	assert.NotNil(t, cat)
	hc := NewHealthChecker([]*Category{cat})
	assert.NotNil(t, hc)

	// Test
	results := hc.Run().GetResults()

	assert.Len(t, results, 2)
	assert.True(t, called)
}

func TestHealthCheckerFatal(t *testing.T) {

	called := false
	// Set up
	checkers := []*Checker{
		{
			Description: "Checker 123",
			HintAnchor:  "check123",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return fmt.Errorf("ERROR")
			},
			Fatal: true,
		},
		{
			Description: "Checker 234",
			HintAnchor:  "check234",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				called = true
				return nil
			},
		},
	}

	cat := NewCategory("test", checkers, true, "http://test.com/")
	assert.NotNil(t, cat)
	hc := NewHealthChecker([]*Category{cat})
	assert.NotNil(t, hc)

	// Test
	results := hc.Run().GetResults()

	assert.Len(t, results, 1)
	assert.False(t, called)
}

func TestHealthCheckerRetry(t *testing.T) {

	retryDeadline := time.Millisecond * 100
	counter := 0

	// Set up
	checkers := []*Checker{
		{
			Description: "Checker 123",
			HintAnchor:  "check123",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				counter++
				return fmt.Errorf("ERROR")
			},
			RetryDeadline: retryDeadline,
		},
		{
			Description: "Checker 234",
			HintAnchor:  "check234",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return nil
			},
		},
	}

	cat := NewCategory("test", checkers, true, "http://test.com/")
	assert.NotNil(t, cat)
	hc := NewHealthChecker([]*Category{cat}).
		WithRetryWindow(time.Millisecond)
	assert.NotNil(t, hc)

	// Test
	results := hc.Run().GetResults()

	assert.Len(t, results, counter+1)

	// picked 5 out of the air. Really all we want is a few times to retry
	// but it all depends on the scheduler. So on a non-busy system, it should
	// be rescheduled RetryDeadline / DefaultRetryWindo times
	assert.GreaterOrEqual(t, counter, 5)

	// Confirm that the last retry has failed
	assert.Error(t, results[counter-1].Err)

	// Confirm that the last check worked
	assert.NoError(t, results[counter].Err)
}

func TestHealthCheckerSkip(t *testing.T) {

	// Set up
	checkers := []*Checker{
		{
			Description: "Checker 123",
			HintAnchor:  "check123",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return SkipError{
					Reason: "Skip Test",
				}
			},

			// Make it fatal, but it will not stop since it should be skipped
			Fatal: true,
		},
		{
			Description: "Checker 234",
			HintAnchor:  "check234",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return nil
			},
		},
	}

	cat := NewCategory("test", checkers, true, "http://test.com/")
	assert.NotNil(t, cat)
	hc := NewHealthChecker([]*Category{cat})
	assert.NotNil(t, hc)

	// Test
	results := hc.Run().GetResults()

	// Only has 1 becase it skipped the first one.
	assert.Len(t, results, 1)

	// Assert that the only result is the non-skipped check
	result := results[0]
	assert.Equal(t, result.Category, CategoryID("test"))
	assert.Equal(t, result.Description, "Checker 234")
	assert.Equal(t, result.HintURL, "http://test.com/check234")
}

func TestHealthCheckerVerboseSuccess(t *testing.T) {

	// Set up
	checkers := []*Checker{
		{
			Description: "Checker 123",
			HintAnchor:  "check123",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return VerboseSuccess{
					Message: "Hello",
				}
			},
		},
		{
			Description: "Checker 234",
			HintAnchor:  "check234",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return fmt.Errorf("ERROR")
			},
		},
	}

	cat := NewCategory("test", checkers, true, "http://test.com/")
	assert.NotNil(t, cat)
	hc := NewHealthChecker([]*Category{cat})
	assert.NotNil(t, hc)

	// Test
	results := hc.Run().GetResults()

	assert.Len(t, results, 2)

	result := results[0]
	assert.Equal(t, result.Category, CategoryID("test"))
	assert.Equal(t, result.Description, "Checker 123\nHello")
	assert.Equal(t, result.HintURL, "http://test.com/check123")
	assert.False(t, result.Retry)
	assert.False(t, result.Warning)
	assert.NoError(t, result.Err)

	result = results[1]
	assert.Equal(t, result.Category, CategoryID("test"))
	assert.Equal(t, result.Description, "Checker 234")
	assert.Equal(t, result.HintURL, "http://test.com/check234")
	assert.False(t, result.Retry)
	assert.False(t, result.Warning)
	assert.Error(t, result.Err)
	assert.ErrorAs(t, result.Err, &CategoryError{})

}

func TestHealthCheckerPrinter(t *testing.T) {

	// Set up
	checkers := []*Checker{
		{
			Description: "Checker 123",
			HintAnchor:  "check123",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return VerboseSuccess{
					Message: "Hello",
				}
			},
		},
		{
			Description: "Checker 234",
			HintAnchor:  "check234",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return fmt.Errorf("This is normally ok, but could be better")
			},
			Warning: true,
		},
		{
			Description: "Checker 2",
			HintAnchor:  "check2",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return fmt.Errorf("bad")
			},
		},
		{
			Description: "Checker 3",
			HintAnchor:  "check3",
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return nil
			},
		},
		{
			Description: "Checker 4",
			HintAnchor:  "check4",
			Warning:     true,
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return nil
			},
		},
		{
			Description: "Checker 5",
			HintAnchor:  "check5",
			Warning:     true,
			Check: func(ctx context.Context, state *HealthCheckState) error {
				return fmt.Errorf("ERROR")
			},
		},
	}

	cat := NewCategory("test", checkers, true, "http://test.com/")
	assert.NotNil(t, cat)
	cat2 := NewCategory("test2", checkers, true, "http://test.com/")
	assert.NotNil(t, cat2)
	hc := NewHealthChecker([]*Category{cat, cat2})
	assert.NotNil(t, hc)

	// Test
	reporter := hc.Run()
	b, err := reporter.ToJSON()
	assert.NoError(t, err)

	var c common.HealthCheckResults
	err = json.Unmarshal(b, &c)
	assert.NoError(t, err)

	assert.Len(t, c.Categories, 2)
	assert.False(t, c.Success)
	assert.True(t, c.Warning)

	category := c.Categories[0]
	assert.Equal(t, category.Name, "test")
	assert.Len(t, category.Checks, len(checkers))

	check := category.Checks[0]
	assert.Equal(t, check.Description, "Checker 123\nHello")
	assert.Equal(t, check.Result, common.HealthCheckResultTypes_SUCCESS)
	assert.Empty(t, check.Error)

	check = category.Checks[1]
	assert.Equal(t, check.Description, "Checker 234")
	assert.Equal(t, check.Result, common.HealthCheckResultTypes_WARNING)
	assert.NotEmpty(t, check.Error)

	check = category.Checks[2]
	assert.Equal(t, check.Description, "Checker 2")
	assert.Equal(t, check.Result, common.HealthCheckResultTypes_ERROR)
	assert.Equal(t, check.Error, "bad")

	check = category.Checks[4]
	assert.Equal(t, check.Description, "Checker 4")
	assert.Equal(t, check.Result, common.HealthCheckResultTypes_SUCCESS)

	check = category.Checks[5]
	assert.Equal(t, check.Description, "Checker 5")
	assert.Equal(t, check.Result, common.HealthCheckResultTypes_WARNING)
}
