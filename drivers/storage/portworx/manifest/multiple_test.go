package manifest

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestErrorWhenReadingLocalManifest(t *testing.T) {
	defer unmaskLoadManifest()
	// Error reading manifest file
	readManifest = func(filename string) ([]byte, error) {
		return nil, fmt.Errorf("file read error")
	}
	r, err := newDeprecatedManifest("1.2.3").Get()
	require.EqualError(t, err, "file read error")
	require.Nil(t, r)

	// Error parsing manifest file
	maskLoadManifest("invalid-yaml")
	r, err = newDeprecatedManifest("1.2.3").Get()
	require.Error(t, err)
	require.Nil(t, r)
}

func TestManifestWithoutReleases(t *testing.T) {
	defer unmaskLoadManifest()
	// Empty manifest file
	maskLoadManifest("")
	r, err := newDeprecatedManifest("1.2.3").Get()
	require.Equal(t, ErrEmptyReleases, err)
	require.Nil(t, r)

	// No releases section
	maskLoadManifest(`
defaultRelease: 1.2.3
`)
	r, err = newDeprecatedManifest("1.2.3").Get()
	require.Equal(t, ErrEmptyReleases, err)
	require.Nil(t, r)

	// Empty releases list
	maskLoadManifest(`
defaultRelease: 1.2.3
releases:
`)
	r, err = newDeprecatedManifest("1.2.3").Get()
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
`)
	r, err := newDeprecatedManifest("1.2.3").Get()
	require.Equal(t, ErrInvalidDefaultRelease, err)
	require.Nil(t, r)
}

func TestValidLocalManifest(t *testing.T) {
	defer unmaskLoadManifest()
	maskLoadManifest(`
defaultRelease: 1.2.3
releases:
  1.2.3:
    stork: stork/image:1.2.3
    lighthouse: lighthouse/image:1.2.3
    autopilot: autopilot/image:1.2.3
  1.2.4:
    stork: stork/image:1.2.4
    lighthouse: lighthouse/image:1.2.4
    autopilot: autopilot/image:1.2.4
`)

	r, err := newDeprecatedManifest("1.2.3").Get()
	require.NoError(t, err)
	require.Equal(t, "1.2.3", r.PortworxVersion)
	require.Equal(t, "stork/image:1.2.3", r.Components.Stork)
	require.Equal(t, "lighthouse/image:1.2.3", r.Components.Lighthouse)
	require.Equal(t, "autopilot/image:1.2.3", r.Components.Autopilot)

	r, err = newDeprecatedManifest("1.2.4").Get()
	require.NoError(t, err)
	require.Equal(t, "1.2.4", r.PortworxVersion)
	require.Equal(t, "stork/image:1.2.4", r.Components.Stork)
	require.Equal(t, "lighthouse/image:1.2.4", r.Components.Lighthouse)
	require.Equal(t, "autopilot/image:1.2.4", r.Components.Autopilot)
}

func TestGetWithReleaseManifestWithInvalidVersions(t *testing.T) {
	defer unmaskLoadManifest()
	maskLoadManifest(`
releases:
  master:
    lighthouse: lighthouse/image:master
  1.2.3:
    lighthouse: lighthouse/image:1.2.3
`)

	// Should return the release if found even if it's not semvar
	release, err := newDeprecatedManifest("master").Get()
	require.NoError(t, err)
	require.Equal(t, "lighthouse/image:master", release.Components.Lighthouse)
	require.Equal(t, "master", release.PortworxVersion)

	// Should return error if not found
	release, err = newDeprecatedManifest("1.2.3.1").Get()
	require.Equal(t, ErrReleaseNotFound, err)
	require.Nil(t, release)

	// Should return error if not found directly and is invalid semver
	release, err = newDeprecatedManifest("latest").Get()
	require.Contains(t, err.Error(), "invalid version")
	require.Nil(t, release)
}

func TestGetBySemvarVersionOnReleaseManifest(t *testing.T) {
	defer unmaskLoadManifest()
	maskLoadManifest(`
releases:
  master:
    autopilot: autopilot/image:master
  1.2.3.0:
    autopilot: autopilot/image:1.2.3.0
`)

	// Should return err if empty version passed
	release, err := newDeprecatedManifest("").Get()
	require.Contains(t, err.Error(), "invalid version")
	require.Nil(t, release)

	// Should return a matching version found
	release, err = newDeprecatedManifest("1.2.3.0").Get()
	require.NoError(t, err)
	require.Equal(t, "autopilot/image:1.2.3.0", release.Components.Autopilot)
	require.Equal(t, "1.2.3.0", release.PortworxVersion)

	// Should return even if a matching semver version found
	release, err = newDeprecatedManifest("1.2.3").Get()
	require.NoError(t, err)
	require.Equal(t, "autopilot/image:1.2.3.0", release.Components.Autopilot)
	require.Equal(t, "1.2.3", release.PortworxVersion)

	// Should return if semvar does not match exactly
	release, err = newDeprecatedManifest("1.2").Get()
	require.Equal(t, ErrReleaseNotFound, err)
	require.Nil(t, release)

	// Should return err if not found
	release, err = newDeprecatedManifest("1.2.3.1").Get()
	require.Equal(t, ErrReleaseNotFound, err)
	require.Nil(t, release)
}

func TestReadingLocalManifestFile(t *testing.T) {
	setupHTTPFailure()
	linkPath := path.Join(
		os.Getenv("GOPATH"),
		"src/github.com/libopenstorage/operator/drivers/storage/portworx/manifest/testspec",
	)
	err := os.Symlink(linkPath, manifestDir)
	require.NoError(t, err)

	defer func() {
		os.RemoveAll(manifestDir)
	}()

	r, err := newDeprecatedManifest("2.1.5").Get()
	require.NoError(t, err)
	require.Equal(t, "2.1.5", r.PortworxVersion)
	require.Equal(t, "openstorage/stork:2.2.4", r.Components.Stork)
	require.Equal(t, "portworx/px-lighthouse:2.0.4", r.Components.Lighthouse)
	require.Equal(t, "portworx/autopilot:0.0.0", r.Components.Autopilot)
}

func TestRemoteManifest(t *testing.T) {
	linkPath := path.Join(
		os.Getenv("GOPATH"),
		"src/github.com/libopenstorage/operator/drivers/storage/portworx/manifest/testspec",
	)
	err := os.Symlink(linkPath, manifestDir)
	require.NoError(t, err)
	os.Remove(path.Join(linkPath, remoteReleaseManifest))

	defer func() {
		os.Remove(path.Join(linkPath, remoteReleaseManifest))
		os.RemoveAll(manifestDir)
		unmaskLoadManifest()
		setupHTTPFailure()
	}()

	// Should download remote manifest if not present
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader([]byte(`
releases:
  3.2.1:
    stork: stork/image:3.2.1
`))),
		}, nil
	}

	r, err := newDeprecatedManifest("3.2.1").Get()
	require.NoError(t, err)

	require.Equal(t, "3.2.1", r.PortworxVersion)
	require.Equal(t, "stork/image:3.2.1", r.Components.Stork)

	// If the manifest is stale, and the download fails then return the stale manifest
	setupHTTPFailure()

	r, err = newDeprecatedManifest("3.2.1").Get()
	require.NoError(t, err)

	require.Equal(t, "3.2.1", r.PortworxVersion)
	require.Equal(t, "stork/image:3.2.1", r.Components.Stork)

	// If manifest is stale, download fails and unable to read previous file,
	// then load the local manifest file
	setupHTTPFailure()
	readManifest = func(filename string) ([]byte, error) {
		if strings.Contains(filename, remoteReleaseManifest) {
			return nil, fmt.Errorf("remote manifest read error")
		}
		return readReleaseManifest(filename)
	}

	r, err = newDeprecatedManifest("2.1.5").Get()
	require.NoError(t, err)

	require.Equal(t, "2.1.5", r.PortworxVersion)
	require.Equal(t, "openstorage/stork:2.2.4", r.Components.Stork)
	require.Equal(t, "portworx/px-lighthouse:2.0.4", r.Components.Lighthouse)
	require.Equal(t, "portworx/autopilot:0.0.0", r.Components.Autopilot)
}

func TestInvalidRemoteManifest(t *testing.T) {
	linkPath := path.Join(
		os.Getenv("GOPATH"),
		"src/github.com/libopenstorage/operator/drivers/storage/portworx/manifest/testspec",
	)
	err := os.Symlink(linkPath, manifestDir)
	require.NoError(t, err)
	os.Remove(path.Join(linkPath, remoteReleaseManifest))

	defer func() {
		os.Remove(path.Join(linkPath, remoteReleaseManifest))
		os.RemoveAll(manifestDir)
		unmaskLoadManifest()
		setupHTTPFailure()
	}()

	// If the downloaded manifest is invalid and unable to read local
	// manifest, return error
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader([]byte("invalid-yaml"))),
		}, nil
	}

	readManifest = func(filename string) ([]byte, error) {
		return nil, fmt.Errorf("read error")
	}

	r, err := newDeprecatedManifest("2.0.0").Get()
	require.Error(t, err)
	require.Nil(t, r)

	// If downloaded manifest is invalid and no existing remote manifest present
	// then use the local manifest
	unmaskLoadManifest()

	r, err = newDeprecatedManifest("2.1.5").Get()
	require.NoError(t, err)

	require.Equal(t, "2.1.5", r.PortworxVersion)
	require.Equal(t, "openstorage/stork:2.2.4", r.Components.Stork)
	require.Equal(t, "portworx/px-lighthouse:2.0.4", r.Components.Lighthouse)
	require.Equal(t, "portworx/autopilot:0.0.0", r.Components.Autopilot)

	// If downloaded manifest is invalid, use the existing remote manifest
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader([]byte(`
releases:
  2.0.0:
    stork: stork/image:2.0.0
`))),
		}, nil
	}

	r, err = newDeprecatedManifest("2.0.0").Get()
	require.NoError(t, err)

	require.Equal(t, "2.0.0", r.PortworxVersion)
	require.Equal(t, "stork/image:2.0.0", r.Components.Stork)

	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader([]byte("invalid-yaml"))),
		}, nil
	}

	r, err = newDeprecatedManifest("2.0.0").Get()
	require.NoError(t, err)

	require.Equal(t, "2.0.0", r.PortworxVersion)
	require.Equal(t, "stork/image:2.0.0", r.Components.Stork)
}

func TestFailureToWriteRemoteManifest(t *testing.T) {
	defer unmaskLoadManifest()
	defer setupHTTPFailure()

	// As the manifest directory is not setup, writing the remote manifest will fail
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: io.NopCloser(bytes.NewReader([]byte(`
releases:
  2.0.0:
    stork: stork/image:2.0.0
`))),
		}, nil
	}
	readManifest = func(_ string) ([]byte, error) {
		return nil, fmt.Errorf("read error")
	}

	r, err := newDeprecatedManifest("2.0.0").Get()
	require.Error(t, err)
	require.Nil(t, r)
}

func TestFailureToReadManifestFromResponse(t *testing.T) {
	defer unmaskLoadManifest()
	defer setupHTTPFailure()

	// As the manifest directory is not setup, writing the remote manifest will fail
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: io.NopCloser(&failedReader{}),
		}, nil
	}
	readManifest = func(_ string) ([]byte, error) {
		return nil, fmt.Errorf("read error")
	}

	r, err := newDeprecatedManifest("2.0.0").Get()
	require.Error(t, err)
	require.Nil(t, r)
}

func TestCustomURLToDownloadManifest(t *testing.T) {
	defer unmaskLoadManifest()
	defer setupHTTPFailure()
	readManifest = func(filename string) ([]byte, error) {
		return nil, fmt.Errorf("read error")
	}

	// Use custom url if env variable set
	customURL := "http://custom-url"
	os.Setenv(envKeyReleaseManifestURL, customURL)
	httpGet = func(url string) (*http.Response, error) {
		require.Equal(t, customURL, url)
		return nil, fmt.Errorf("http error")
	}

	_, err := newDeprecatedManifest("2.0.0").Get()
	require.Error(t, err)

	// Use default url if no env variable set
	os.Unsetenv(envKeyReleaseManifestURL)
	httpGet = func(url string) (*http.Response, error) {
		require.Equal(t, defaultReleaseManifestURL, url)
		return nil, fmt.Errorf("http error")
	}

	_, err = newDeprecatedManifest("2.0.0").Get()
	require.Error(t, err)
}

func maskLoadManifest(manifest string) {
	readManifest = func(_ string) ([]byte, error) {
		return []byte(manifest), nil
	}
}

func unmaskLoadManifest() {
	readManifest = readReleaseManifest
}

func manifestCleanup(t *testing.M) {
	os.RemoveAll(manifestDir)
}

func setupHTTPFailure() {
	httpGet = func(_ string) (*http.Response, error) {
		return nil, fmt.Errorf("http error")
	}
}

type failedReader struct{}

func (r *failedReader) Read(_ []byte) (int, error) {
	return 0, fmt.Errorf("Error: Read failed")
}
