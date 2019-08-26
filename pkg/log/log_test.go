package log

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestLog(t *testing.T) {
	testCases := []struct {
		src      string
		expected string
	}{
		{
			src:      "github.com/libopenstorage/operator/cmd/operator.go",
			expected: "github.com/libopenstorage/operator/cmd/operator.go",
		},
		{
			src:      "go/sys/foo/bar/github.com/libopenstorage/operator/cmd/operator.go",
			expected: "github.com/libopenstorage/operator/cmd/operator.go",
		},
		{
			src:      "gith.com/libopenstorage/operator/cmd/operator.go",
			expected: "gith.com/libopenstorage/operator/cmd/operator.go",
		},
		{
			src:      "foo/bar",
			expected: "foo/bar",
		},
	}

	for _, tc := range testCases {
		res := formatFilePath(tc.src)
		require.Equal(t, res, tc.expected)
	}
}
