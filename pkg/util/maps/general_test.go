package maps

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeys(t *testing.T) {
	m := map[int]string{1: "one", 2: "two", 3: "three", 0: "zero"}
	expectedKeys := []int{0, 1, 2, 3}
	assert.Equal(t, expectedKeys, KeysSorted(m))

	// regular Keys() will return _unsorted_ keys
	keys := Keys(m)
	for _, expkey := range expectedKeys {
		assert.Contains(t, keys, expkey)
	}
}
