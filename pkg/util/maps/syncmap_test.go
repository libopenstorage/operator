package maps

import (
	"testing"
	"time"
)

// TestMapConcurrentPutGet_DEMO demonstrates panic when regular map is used
func TestMapConcurrentPutGet_DEMO(t *testing.T) {
	t.Skip("DISABLED -- this demo-test causes PANIC / fatal error: concurrent map writes")
	var m = make(map[string]string)
	go func() {
		for {
			_ = m["x"]
		}
	}()
	go func() {
		for {
			m["y"] = "foo"
		}
	}()
	time.Sleep(time.Second)
}

// TestMapConcurrentPutGet tests concurrent Put/Get access
func TestMapConcurrentPutGet(t *testing.T) {
	m := MakeSyncMap[string, string]()
	go func() {
		for {
			m.Load("x")
		}
	}()
	go func() {
		for {
			m.Store("y", "foo")
		}
	}()
	time.Sleep(time.Second)
}

// TestMapConcurrentPutEnumerate_DEMO demonstrates panic when regular map is used
func TestMapConcurrentPutEnumerate_DEMO(t *testing.T) {
	t.Skip("DISABLED -- this demo-test causes PANIC / fatal error: concurrent map writes")
	var m = make(map[string]string)
	go func() {
		for {
			for range m {
				_ = m["x"]
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()
	go func() {
		for {
			m["y"] = "foo"
		}
	}()
	time.Sleep(time.Second)
}

func TestMapConcurrentPutEnumerate(t *testing.T) {
	m := MakeSyncMap[string, string]()
	go func() {
		for {
			m.Range(func(key, value string) bool {
				m.Load("x")
				return true
			})
			time.Sleep(10 * time.Millisecond)
		}
	}()
	go func() {
		for {
			m.Store("y", "foo")
		}
	}()
	time.Sleep(time.Second)
}
