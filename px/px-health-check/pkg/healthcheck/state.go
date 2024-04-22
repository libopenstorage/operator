package healthcheck

import (
	"fmt"
	"sync"
)

// HealthStateDataKey is the type used as keys
// in HealthCheckState.data
type HealthStateDataKey string

// HealthCheckState will contain any setup needed
// by the cherkers and any information needed to be passed to other checks
type HealthCheckState struct {
	data   map[HealthStateDataKey]any
	rwlock sync.RWMutex
}

// NewHealthCheckState creates a new key val memory cache for the HealthChecker
// framework
func NewHealthCheckState() *HealthCheckState {
	return &HealthCheckState{
		data: make(map[HealthStateDataKey]any),
	}
}

// Set saves a kev-val in the state so that it can be cached and used
// by other checks
func (cs *HealthCheckState) Set(k HealthStateDataKey, v any) *HealthCheckState {
	if cs == nil {
		return nil
	}

	cs.rwlock.Lock()
	defer cs.rwlock.Unlock()

	if _, ok := cs.data[k]; ok {
		panic(fmt.Sprintf("overriding key[%s] is not "+
			"allowed to avoid conflicts. Use Delete() "+
			"first if you want to reset the key", k))
	}
	cs.data[k] = v
	return cs
}

func (cs *HealthCheckState) Get(k HealthStateDataKey) any {
	if cs == nil {
		return nil
	}

	cs.rwlock.RLock()
	defer cs.rwlock.RUnlock()

	return cs.data[k]
}

// Delete deletes a key saved in the cache
func (cs *HealthCheckState) Delete(k HealthStateDataKey) *HealthCheckState {
	if cs == nil {
		return nil
	}

	cs.rwlock.Lock()
	defer cs.rwlock.Unlock()

	delete(cs.data, k)
	return cs
}
