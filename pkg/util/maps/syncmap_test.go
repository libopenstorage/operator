package maps

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBasicSyncMap(t *testing.T) {
	m := MakeSyncMap[string, int]()

	m.Store("a", 1)
	m.Store("b", 2)
	m.Store("c", 3)

	x, has := m.Load("b")
	assert.True(t, has)
	assert.Equal(t, 2, x)

	x, has = m.Load("z")
	assert.False(t, has)
	assert.Equal(t, 0, x)

	x = m.LoadUnchecked("c")
	assert.Equal(t, 3, x)

	m.Store("b", 99)
	x, has = m.Load("b")
	assert.True(t, has)
	assert.Equal(t, 99, x)

	m.Delete("c")

	x, has = m.Load("c")
	assert.False(t, has)
	assert.Equal(t, 0, x)

	x = m.LoadUnchecked("c")
	assert.Equal(t, 0, x)

	m.Range(func(key string, value int) bool {
		if key != "a" && key != "b" {
			t.Errorf("Invalid key %v", key)
		}
		if value != 1 && value != 99 {
			t.Errorf("Invalid value %v", value)
		}
		return true
	})
}

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
	// note -- if no panic, test has suceeded
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
	// note -- if no panic, test has suceeded
}
