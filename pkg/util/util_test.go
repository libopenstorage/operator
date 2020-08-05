package util

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetImageURN(t *testing.T) {
	// TestCase: Empty image
	out := GetImageURN("registry.io", "")
	require.Equal(t, "", out)

	// TestCase: Empty repo and registry
	out = GetImageURN("", "test/image")
	require.Equal(t, "test/image", out)

	// TestCase: Registry without repo but image with repo
	out = GetImageURN("registry.io", "test/image")
	require.Equal(t, "registry.io/test/image", out)

	out = GetImageURN("registry.io/", "test/image")
	require.Equal(t, "registry.io/test/image", out)

	out = GetImageURN("registry.io", "test/this/image")
	require.Equal(t, "registry.io/test/this/image", out)

	// TestCase: Registry and image without repo
	out = GetImageURN("registry.io", "image")
	require.Equal(t, "registry.io/image", out)

	// TestCase: Image with common docker registries
	out = GetImageURN("registry.io", "docker.io/test/image")
	require.Equal(t, "registry.io/test/image", out)

	out = GetImageURN("registry.io", "quay.io/test/this/image")
	require.Equal(t, "registry.io/test/this/image", out)

	out = GetImageURN("registry.io/", "index.docker.io/test/this/image")
	require.Equal(t, "registry.io/test/this/image", out)

	out = GetImageURN("registry.io", "registry-1.docker.io/image")
	require.Equal(t, "registry.io/image", out)

	out = GetImageURN("registry.io/", "registry.connect.redhat.com/image")
	require.Equal(t, "registry.io/image", out)

	// TestCase: Regsitry and image both with repo
	out = GetImageURN("registry.io/repo", "test/image")
	require.Equal(t, "registry.io/repo/image", out)

	out = GetImageURN("registry.io/repo", "test/this/image")
	require.Equal(t, "registry.io/repo/image", out)

	out = GetImageURN("registry.io/repo/", "test/image")
	require.Equal(t, "registry.io/repo/image", out)

	out = GetImageURN("registry.io/repo//", "test/this/image")
	require.Equal(t, "registry.io/repo/image", out)

	// TestCase: Regsitry with repo but image without repo
	out = GetImageURN("registry.io/repo", "image")
	require.Equal(t, "registry.io/repo/image", out)

	out = GetImageURN("registry.io/repo/subdir", "image")
	require.Equal(t, "registry.io/repo/subdir/image", out)

	// TestCase: Registry with empty root repo
	out = GetImageURN("registry.io//", "image")
	require.Equal(t, "registry.io/image", out)

	out = GetImageURN("registry.io//", "test/image")
	require.Equal(t, "registry.io/image", out)

	out = GetImageURN("registry.io//", "test/this/image")
	require.Equal(t, "registry.io/image", out)
}
