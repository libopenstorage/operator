package storagematrix

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func verify(t *testing.T, totalNodes uint64, totalZones uint64, expectedValue uint64) {
	storageNodes, err := GetStorageNodesCount(totalNodes, totalZones)
	require.NoError(t, err, "unexpected error")
	require.Equal(t, storageNodes, expectedValue)
}

func TestGetStorageNodesCount(t *testing.T) {
	// 1 Zone
	verify(t, 3, 1, 3)
	verify(t, 6, 1, 6)
	verify(t, 18, 1, 12)
	verify(t, 20, 1, 13)
	verify(t, 42, 1, 15)
	verify(t, 102, 1, 34)
	verify(t, 390, 1, 99)
	verify(t, 1011, 1, 251)
	verify(t, 5000, 1, 500)

	// 2 Zone
	verify(t, 3, 2, 1)
	verify(t, 6, 2, 3)
	verify(t, 18, 2, 6)
	verify(t, 20, 2, 6)
	verify(t, 42, 2, 7)
	verify(t, 102, 2, 17)
	verify(t, 390, 2, 49)
	verify(t, 1011, 2, 125)
	verify(t, 5000, 2, 250)

	// 3 Zone
	verify(t, 3, 3, 1)
	verify(t, 6, 3, 2)
	verify(t, 18, 3, 4)
	verify(t, 20, 3, 4)
	verify(t, 42, 3, 5)
	verify(t, 102, 3, 11)
	verify(t, 390, 3, 33)
	verify(t, 1011, 3, 83)
	verify(t, 5000, 3, 166)

}
