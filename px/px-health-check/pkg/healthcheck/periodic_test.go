package healthcheck

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewPeriodicHealthChecker(t *testing.T) {
	hc := NewHealthChecker(nil)
	f := func(Reporter) {}
	d := time.Duration(0)

	p := NewPeriodicHealthChecker(d, hc, f)
	assert.Nil(t, p)

	p = NewPeriodicHealthChecker(time.Second, nil, f)
	assert.Nil(t, p)

	p = NewPeriodicHealthChecker(time.Second, hc, nil)
	assert.NotNil(t, p)

	p = NewPeriodicHealthChecker(time.Second, hc, f)
	assert.NotNil(t, p)
}

func TestPeriodicHealthChecker_Run(t *testing.T) {
	hc := NewHealthChecker(nil)
	f := func(Reporter) {}

	p := NewPeriodicHealthChecker(time.Second, hc, f)
	assert.NotNil(t, p)

	assert.Nil(t, p.GetReport())
	assert.False(t, p.IsRunning())
	p.Run()

	var results Reporter
	for ; results == nil; results = p.GetReport() {
		time.Sleep(time.Microsecond)
	}
	assert.NotNil(t, results)
	assert.True(t, results.Successful())

	assert.True(t, p.IsRunning())
	p.Run()
	assert.True(t, p.IsRunning())
	p.Stop()
	for ; results != nil; results = p.GetReport() {
		time.Sleep(time.Microsecond)
	}
	assert.False(t, p.IsRunning())

	results = p.GetReport()
	assert.Nil(t, results)
}

func TestPeriodicHealthCheckerReports(t *testing.T) {
	hc := NewHealthChecker(nil)

	called := false
	reporter := false
	success := false
	var reporterCalled sync.WaitGroup

	reporterCalled.Add(1)
	f := func(r Reporter) {
		if called {
			return
		}

		if r != nil {
			reporter = true
			success = r.Successful()
		}
		called = true
		reporterCalled.Done()
	}

	p := NewPeriodicHealthChecker(time.Microsecond, hc, f)
	assert.NotNil(t, p)

	assert.False(t, p.IsRunning())
	p.Run()
	p.Run()
	p.Run()
	p.Run()
	assert.True(t, p.IsRunning())

	reporterCalled.Wait()
	p.Stop()

	assert.True(t, called)
	assert.True(t, reporter)
	assert.True(t, success)
	assert.False(t, p.IsRunning())

	// Try to stop again
	assert.NotPanics(t, func() {
		p.Stop()
		p.Stop()
		p.Stop()
		p.Stop()
		p.Stop()
	})

	// Start again
	called = false
	reporter = false
	success = false
	reporterCalled.Add(1)
	p.Run()
	assert.True(t, p.IsRunning())

	reporterCalled.Wait()
	p.Stop()
	assert.False(t, p.IsRunning())
}

func TestPeriodicHealthCheckerReportsQuickRestarts(t *testing.T) {
	hc := NewHealthChecker(nil)

	count := 0
	f := func(r Reporter) { count++ }

	p := NewPeriodicHealthChecker(time.Microsecond, hc, f)
	assert.NotNil(t, p)

	for i := 0; i < 100; i++ {
		p.Run()
		var results Reporter
		for ; results == nil; results = p.GetReport() {
			time.Sleep(time.Microsecond)
		}
		p.Stop()
	}
	assert.NotZero(t, count)
}
