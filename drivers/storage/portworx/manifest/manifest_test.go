package manifest

import (
	"fmt"
	"os"
	"path"
	"testing"

	version "github.com/hashicorp/go-version"
	"github.com/stretchr/testify/require"
)

func TestErrorWhenReadingManifest(t *testing.T) {
	// Error reading manifest file
	loadReleaseManifest = func() ([]byte, error) {
		return nil, fmt.Errorf("file read error")
	}
	r, err := NewReleaseManifest()
	require.EqualError(t, err, "file read error")
	require.Nil(t, r)

	// Error parsing manifest file
	defer unmaskLoadManifest()
	maskLoadManifest("invalid-yaml")
	r, err = NewReleaseManifest()
	require.Error(t, err)
	require.Nil(t, r)
}

func TestManifestWithoutReleases(t *testing.T) {
	// Emtpy manifest file
	defer unmaskLoadManifest()
	maskLoadManifest("")
	r, err := NewReleaseManifest()
	require.Equal(t, ErrEmptyReleases, err)
	require.Nil(t, r)

	// No releases section
	maskLoadManifest(`
defaultRelease: 1.2.3
`)
	r, err = NewReleaseManifest()
	require.Equal(t, ErrEmptyReleases, err)
	require.Nil(t, r)

	// Empty releases list
	maskLoadManifest(`
defaultRelease: 1.2.3
releases:
`)
	r, err = NewReleaseManifest()
	require.Equal(t, ErrEmptyReleases, err)
	require.Nil(t, r)
}

func TestInvalidDefaultRelease(t *testing.T) {
	defer unmaskLoadManifest()
	maskLoadManifest(`
defaultRelease: master
releases:
  1.2.3:
    stork: stork/image
    lighthouse: lighthouse/image
`)
	r, err := NewReleaseManifest()
	require.Equal(t, ErrInvalidDefaultRelease, err)
	require.Nil(t, r)
}

func TestValidManifest(t *testing.T) {
	defer unmaskLoadManifest()
	maskLoadManifest(`
defaultRelease: 1.2.3
releases:
  1.2.3:
    stork: stork/image:1.2.3
    lighthouse: lighthouse/image:1.2.3
  1.2.4:
    stork: stork/image:1.2.4
    lighthouse: lighthouse/image:1.2.4
`)

	r, err := NewReleaseManifest()
	require.NoError(t, err)
	require.Equal(t, "1.2.3", r.DefaultRelease)
	require.Len(t, r.Releases, 2)
	require.Equal(t, "stork/image:1.2.3", r.Releases["1.2.3"].Stork)
	require.Equal(t, "lighthouse/image:1.2.3", r.Releases["1.2.3"].Lighthouse)
	require.Equal(t, "stork/image:1.2.4", r.Releases["1.2.4"].Stork)
	require.Equal(t, "lighthouse/image:1.2.4", r.Releases["1.2.4"].Lighthouse)
}

func TestMissingDefaultShouldMakeLatestAsDefault(t *testing.T) {
	defer unmaskLoadManifest()
	// Choose the latest version as default if absent
	maskLoadManifest(`
releases:
  1.2.3:
    stork: stork/image:1.2.3
    lighthouse: lighthouse/image:1.2.3
  1.2.4:
    stork: stork/image:1.2.4
    lighthouse: lighthouse/image:1.2.4
  1.2.4-rc1:
    stork: stork/image:1.2.4-rc1
    lighthouse: lighthouse/image:1.2.4-rc1
`)

	r, err := NewReleaseManifest()
	require.NoError(t, err)
	require.Len(t, r.Releases, 3)
	require.Equal(t, "1.2.4", r.DefaultRelease)

	release, err := r.GetDefault()
	require.NoError(t, err)
	require.Equal(t, "stork/image:1.2.4", release.Stork)
	require.Equal(t, "lighthouse/image:1.2.4", release.Lighthouse)

	// Non-semvar versions are considered newer than semvar versions.
	// Non-semvar versions are sorted amongst themselves lexicographically.
	maskLoadManifest(`
releases:
  test1:
    stork: stork/image:test1
    lighthouse: lighthouse/image:test1
  1.2.3:
    stork: stork/image:1.2.3
    lighthouse: lighthouse/image:1.2.3
  test3:
    stork: stork/image:test3
    lighthouse: lighthouse/image:test3
  test2:
    stork: stork/image:test2
    lighthouse: lighthouse/image:test2
`)

	r, err = NewReleaseManifest()
	require.NoError(t, err)
	require.Len(t, r.Releases, 4)
	require.Equal(t, "test3", r.DefaultRelease)

	release, err = r.GetDefault()
	require.NoError(t, err)
	require.Equal(t, "stork/image:test3", release.Stork)
	require.Equal(t, "lighthouse/image:test3", release.Lighthouse)
}

func TestGetOnReleaseManifest(t *testing.T) {
	defer unmaskLoadManifest()
	maskLoadManifest(`
releases:
  master:
    stork: stork/image:master
    lighthouse: lighthouse/image:master
  1.2.3:
    stork: stork/image:1.2.3
    lighthouse: lighthouse/image:1.2.3
`)

	r, err := NewReleaseManifest()
	require.NoError(t, err)

	// Should return the release if found
	release, err := r.Get("master")
	require.NoError(t, err)
	require.Equal(t, "stork/image:master", release.Stork)
	require.Equal(t, "lighthouse/image:master", release.Lighthouse)

	// Should return a matching semver release if not found directly
	release, err = r.Get("1.2.3.0")
	require.NoError(t, err)
	require.Equal(t, "stork/image:1.2.3", release.Stork)
	require.Equal(t, "lighthouse/image:1.2.3", release.Lighthouse)

	// Should return error if not found
	release, err = r.Get("1.2.3.1")
	require.Equal(t, ErrReleaseNotFound, err)
	require.Nil(t, release)

	// Should return error if not found directly and is invalid semver
	release, err = r.Get("latest")
	require.Contains(t, err.Error(), "invalid version")
	require.Nil(t, release)
}

func TestGetFromVersionOnReleaseManifest(t *testing.T) {
	defer unmaskLoadManifest()
	maskLoadManifest(`
releases:
  master:
    stork: stork/image:master
    lighthouse: lighthouse/image:master
  1.2.3.0:
    stork: stork/image:1.2.3.0
    lighthouse: lighthouse/image:1.2.3.0
`)

	r, err := NewReleaseManifest()
	require.NoError(t, err)

	// Should return err if nil version passed
	release, err := r.GetFromVersion(nil)
	require.Equal(t, ErrReleaseNotFound, err)
	require.Nil(t, release)

	// Should return a matching version found
	v, _ := version.NewSemver("1.2.3.0")
	release, err = r.GetFromVersion(v)
	require.NoError(t, err)
	require.Equal(t, "stork/image:1.2.3.0", release.Stork)
	require.Equal(t, "lighthouse/image:1.2.3.0", release.Lighthouse)

	// Should return even if a matching semver version found
	v, _ = version.NewSemver("1.2.3")
	release, err = r.GetFromVersion(v)
	require.NoError(t, err)
	require.Equal(t, "stork/image:1.2.3.0", release.Stork)
	require.Equal(t, "lighthouse/image:1.2.3.0", release.Lighthouse)

	// Should return err if not found
	v, _ = version.NewSemver("1.2.3.1")
	release, err = r.GetFromVersion(v)
	require.Equal(t, ErrReleaseNotFound, err)
	require.Nil(t, release)
}

func TestReadingManifestFile(t *testing.T) {
	os.RemoveAll(ManifestDir)
	linkPath := path.Join(
		os.Getenv("GOPATH"),
		"src/github.com/libopenstorage/operator/drivers/storage/portworx/manifest/testspec",
	)
	os.Symlink(linkPath, ManifestDir)

	r, err := NewReleaseManifest()
	require.NoError(t, err)
	require.Equal(t, "2.1.5", r.DefaultRelease)
	require.Len(t, r.Releases, 1)
	require.Equal(t, "openstorage/stork:2.2.4", r.Releases["2.1.5"].Stork)
	require.Equal(t, "portworx/px-lighthouse:2.0.4", r.Releases["2.1.5"].Lighthouse)

	os.RemoveAll(ManifestDir)
}

func maskLoadManifest(manifest string) {
	loadReleaseManifest = func() ([]byte, error) {
		return []byte(manifest), nil
	}
}

func unmaskLoadManifest() {
	loadReleaseManifest = readManifestFile
}
