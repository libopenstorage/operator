package healthcheck

import (
	"sync"
	"time"
)

// PeriodicCompletion is a callback function used to send the results of each
// periodic health check run
type PeriodicReporter func(Reporter)

// PeriodicHealthChecker is a struct that represents a periodic health checker
type PeriodicHealthChecker struct {
	d         time.Duration
	stop      chan struct{}
	isrunning bool
	lock      sync.Mutex
	runLock   sync.RWMutex
	hc        *HealthChecker
	f         PeriodicReporter
	report    Reporter
	done      sync.WaitGroup
}

// NewPeriodicHealthChecker runs a health check periodically and calls a
// PeriodicReporter function with the results of every health check run.
// Passing a PeriodicReporter function is optional.
func NewPeriodicHealthChecker(
	d time.Duration,
	hc *HealthChecker,
	f PeriodicReporter,
) *PeriodicHealthChecker {

	if hc == nil || d == time.Duration(0) {
		return nil
	}

	return &PeriodicHealthChecker{
		d:    d,
		hc:   hc,
		f:    f,
		stop: make(chan struct{}, 1),
	}
}

// Run is a function that starts the periodic health checker
func (p *PeriodicHealthChecker) Run() {
	p.lock.Lock()
	defer p.lock.Unlock()

	if p.isrunning {
		return
	}
	p.isrunning = true

	p.done.Wait()
	p.done.Add(1)
	go p.run()
}

// Stop is a function that stops the periodic health checker
func (p *PeriodicHealthChecker) Stop() {
	p.lock.Lock()
	defer p.lock.Unlock()

	if p.isrunning {
		p.stop <- struct{}{}
		p.isrunning = false
	}
}

// IsRunning is a function that returns whether the periodic health checker is running
func (p *PeriodicHealthChecker) IsRunning() bool {
	p.lock.Lock()
	defer p.lock.Unlock()

	return p.isrunning
}

// GetReport is a function that returns the last health check report
// This function blocks while tests are running.
// This function may return nil if the periodic health checker has not run yet.
func (p *PeriodicHealthChecker) GetReport() Reporter {
	p.runLock.RLock()
	defer p.runLock.RUnlock()

	return p.report
}

// run is a function that runs the periodic health checker according to a timer channel
func (p *PeriodicHealthChecker) run() {
	// Run immediately the first time
	p.runchecks()

	// Create a ticker to run periodically
	ticker := time.NewTicker(p.d)

	// run the periodic health checker until the stop channel is closed
	for {
		select {
		case <-ticker.C:
			p.runchecks()

			// call the reporter function
			if p.f != nil {
				p.f(p.GetReport())
			}
		case <-p.stop:
			// stop the periodic health checker
			p.clearReport()
			ticker.Stop()
			p.done.Done()
			return
		}
	}
}

func (p *PeriodicHealthChecker) runchecks() {
	p.runLock.Lock()
	defer p.runLock.Unlock()

	// run the health checks
	p.report = p.hc.Run()
}

func (p *PeriodicHealthChecker) clearReport() {
	p.runLock.Lock()
	defer p.runLock.Unlock()

	p.report = nil
}
