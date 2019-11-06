package manifest

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	version "github.com/hashicorp/go-version"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	manifestCleanup(m)
	setupHTTPFailure()
	code := m.Run()
	manifestCleanup(m)
	httpGet = http.Get
	os.Exit(code)
}

func TestErrorWhenReadingLocalManifest(t *testing.T) {
	defer unmaskLoadManifest()
	// Error reading manifest file
	readManifest = func(filename string) ([]byte, error) {
		require.Equal(t, path.Join(ManifestDir, LocalReleaseManifest), filename)
		return nil, fmt.Errorf("file read error")
	}
	r, err := NewReleaseManifest()
	require.EqualError(t, err, "file read error")
	require.Nil(t, r)

	// Error parsing manifest file
	maskLoadManifest("invalid-yaml")
	r, err = NewReleaseManifest()
	require.Error(t, err)
	require.Nil(t, r)
}

func TestManifestWithoutReleases(t *testing.T) {
	defer unmaskLoadManifest()
	// Empty manifest file
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
`)
	r, err := NewReleaseManifest()
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

	r, err := NewReleaseManifest()
	require.NoError(t, err)
	require.Equal(t, "1.2.3", r.DefaultRelease)
	require.Len(t, r.Releases, 2)
	require.Equal(t, "stork/image:1.2.3", r.Releases["1.2.3"].Stork)
	require.Equal(t, "lighthouse/image:1.2.3", r.Releases["1.2.3"].Lighthouse)
	require.Equal(t, "autopilot/image:1.2.3", r.Releases["1.2.3"].Autopilot)
	require.Equal(t, "stork/image:1.2.4", r.Releases["1.2.4"].Stork)
	require.Equal(t, "lighthouse/image:1.2.4", r.Releases["1.2.4"].Lighthouse)
	require.Equal(t, "autopilot/image:1.2.4", r.Releases["1.2.4"].Autopilot)
}

func TestMissingDefaultShouldMakeLatestAsDefault(t *testing.T) {
	defer unmaskLoadManifest()
	// Choose the latest version as default if absent
	maskLoadManifest(`
releases:
  1.2.3:
    stork: stork/image:1.2.3
  1.2.4:
    stork: stork/image:1.2.4
  1.2.4-rc1:
    stork: stork/image:1.2.4-rc1
`)

	r, err := NewReleaseManifest()
	require.NoError(t, err)
	require.Len(t, r.Releases, 3)
	require.Equal(t, "1.2.4", r.DefaultRelease)

	release, err := r.GetDefault()
	require.NoError(t, err)
	require.Equal(t, "stork/image:1.2.4", release.Stork)

	// Non-semvar versions are considered newer than semvar versions.
	// Non-semvar versions are sorted amongst themselves lexicographically.
	maskLoadManifest(`
releases:
  test1:
    stork: stork/image:test1
  1.2.3:
    stork: stork/image:1.2.3
  test3:
    stork: stork/image:test3
  test2:
    stork: stork/image:test2
`)

	r, err = NewReleaseManifest()
	require.NoError(t, err)
	require.Len(t, r.Releases, 4)
	require.Equal(t, "test3", r.DefaultRelease)

	release, err = r.GetDefault()
	require.NoError(t, err)
	require.Equal(t, "stork/image:test3", release.Stork)
}

func TestGetOnReleaseManifest(t *testing.T) {
	defer unmaskLoadManifest()
	maskLoadManifest(`
releases:
  master:
    lighthouse: lighthouse/image:master
  1.2.3:
    lighthouse: lighthouse/image:1.2.3
`)

	r, err := NewReleaseManifest()
	require.NoError(t, err)

	// Should return the release if found
	release, err := r.Get("master")
	require.NoError(t, err)
	require.Equal(t, "lighthouse/image:master", release.Lighthouse)

	// Should return a matching semver release if not found directly
	release, err = r.Get("1.2.3.0")
	require.NoError(t, err)
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
    autopilot: autopilot/image:master
  1.2.3.0:
    autopilot: autopilot/image:1.2.3.0
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
	require.Equal(t, "autopilot/image:1.2.3.0", release.Autopilot)

	// Should return even if a matching semver version found
	v, _ = version.NewSemver("1.2.3")
	release, err = r.GetFromVersion(v)
	require.NoError(t, err)
	require.Equal(t, "autopilot/image:1.2.3.0", release.Autopilot)

	// Should return err if not found
	v, _ = version.NewSemver("1.2.3.1")
	release, err = r.GetFromVersion(v)
	require.Equal(t, ErrReleaseNotFound, err)
	require.Nil(t, release)
}

func TestReadingLocalManifestFile(t *testing.T) {
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
	require.Equal(t, "portworx/autopilot:0.0.0", r.Releases["2.1.5"].Autopilot)
}

func TestRemoteManifest(t *testing.T) {
	linkPath := path.Join(
		os.Getenv("GOPATH"),
		"src/github.com/libopenstorage/operator/drivers/storage/portworx/manifest/testspec",
	)
	os.Symlink(linkPath, ManifestDir)
	os.Remove(path.Join(linkPath, RemoteReleaseManifest))

	defer func() {
		os.Remove(path.Join(linkPath, RemoteReleaseManifest))
		os.RemoveAll(ManifestDir)
		unmaskLoadManifest()
		setupHTTPFailure()
		refreshInterval = manifestRefreshInterval
	}()

	// Should download remote manifest if not present
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte(`
releases:
  3.2.1:
    stork: stork/image:3.2.1
`))),
		}, nil
	}

	r, err := NewReleaseManifest()
	require.NoError(t, err)

	require.Equal(t, "3.2.1", r.DefaultRelease)
	require.Len(t, r.Releases, 1)
	require.Equal(t, "stork/image:3.2.1", r.Releases["3.2.1"].Stork)

	// Should load the same file again instead of downloading it again
	// We are mocking the http call to return error so we know it is not called
	setupHTTPFailure()

	r, err = NewReleaseManifest()
	require.NoError(t, err)

	require.Equal(t, "3.2.1", r.DefaultRelease)
	require.Len(t, r.Releases, 1)
	require.Equal(t, "stork/image:3.2.1", r.Releases["3.2.1"].Stork)

	// If loading the existing remote manifest fails, we should download it again
	readManifest = func(filename string) ([]byte, error) {
		return nil, fmt.Errorf("remote manifest read error")
	}
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte(`
releases:
  3.5.0:
    stork: stork/image:3.5.0
`))),
		}, nil
	}

	r, err = NewReleaseManifest()
	require.NoError(t, err)

	require.Equal(t, "3.5.0", r.DefaultRelease)
	require.Len(t, r.Releases, 1)
	require.Equal(t, "stork/image:3.5.0", r.Releases["3.5.0"].Stork)
	unmaskLoadManifest()

	// If the manifest is stale, then re-download it
	refreshInterval = func() time.Duration {
		return 0 * time.Second
	}
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte(`
releases:
  4.0.0:
    stork: stork/image:4.0.0
`))),
		}, nil
	}

	r, err = NewReleaseManifest()
	require.NoError(t, err)

	require.Equal(t, "4.0.0", r.DefaultRelease)
	require.Len(t, r.Releases, 1)
	require.Equal(t, "stork/image:4.0.0", r.Releases["4.0.0"].Stork)

	// If the manifest is stale, and the download fails then return the stale manifest
	setupHTTPFailure()

	r, err = NewReleaseManifest()
	require.NoError(t, err)

	require.Equal(t, "4.0.0", r.DefaultRelease)
	require.Len(t, r.Releases, 1)
	require.Equal(t, "stork/image:4.0.0", r.Releases["4.0.0"].Stork)

	// If manifest is stale, download fails and unable to read previous file,
	// then load the local manifest file
	setupHTTPFailure()
	readManifest = func(filename string) ([]byte, error) {
		if strings.Contains(filename, RemoteReleaseManifest) {
			return nil, fmt.Errorf("remote manifest read error")
		}
		return readReleaseManifest(filename)
	}

	r, err = NewReleaseManifest()
	require.NoError(t, err)

	require.Equal(t, "2.1.5", r.DefaultRelease)
	require.Len(t, r.Releases, 1)
	require.Equal(t, "openstorage/stork:2.2.4", r.Releases["2.1.5"].Stork)
	require.Equal(t, "portworx/px-lighthouse:2.0.4", r.Releases["2.1.5"].Lighthouse)
	require.Equal(t, "portworx/autopilot:0.0.0", r.Releases["2.1.5"].Autopilot)
}

func TestInvalidRemoteManifest(t *testing.T) {
	linkPath := path.Join(
		os.Getenv("GOPATH"),
		"src/github.com/libopenstorage/operator/drivers/storage/portworx/manifest/testspec",
	)
	os.Symlink(linkPath, ManifestDir)
	os.Remove(path.Join(linkPath, RemoteReleaseManifest))

	defer func() {
		os.Remove(path.Join(linkPath, RemoteReleaseManifest))
		os.RemoveAll(ManifestDir)
		unmaskLoadManifest()
		setupHTTPFailure()
		refreshInterval = manifestRefreshInterval
	}()

	// If the downloaded manifest is invalid and unable to read local
	// manifest, return error
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte("invalid-yaml"))),
		}, nil
	}

	readManifest = func(filename string) ([]byte, error) {
		return nil, fmt.Errorf("read error")
	}

	r, err := NewReleaseManifest()
	require.Error(t, err)
	require.Nil(t, r)

	// If downloaded manifest is invalid and no existing remote manifest present
	// then use the local manifest
	unmaskLoadManifest()

	r, err = NewReleaseManifest()
	require.NoError(t, err)

	require.Equal(t, "2.1.5", r.DefaultRelease)
	require.Len(t, r.Releases, 1)
	require.Equal(t, "openstorage/stork:2.2.4", r.Releases["2.1.5"].Stork)
	require.Equal(t, "portworx/px-lighthouse:2.0.4", r.Releases["2.1.5"].Lighthouse)
	require.Equal(t, "portworx/autopilot:0.0.0", r.Releases["2.1.5"].Autopilot)

	// If downloaded manifest is invalid, use the existing remote manifest
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte(`
releases:
  2.0.0:
    stork: stork/image:2.0.0
`))),
		}, nil
	}
	_, err = NewReleaseManifest()
	require.NoError(t, err)

	refreshInterval = func() time.Duration {
		return 0 * time.Second
	}
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte("invalid-yaml"))),
		}, nil
	}

	r, err = NewReleaseManifest()
	require.NoError(t, err)

	require.Equal(t, "2.0.0", r.DefaultRelease)
	require.Len(t, r.Releases, 1)
	require.Equal(t, "stork/image:2.0.0", r.Releases["2.0.0"].Stork)
}

func TestFailureToWriteRemoteManifest(t *testing.T) {
	defer unmaskLoadManifest()
	defer setupHTTPFailure()

	// As the manifest directory is not setup, writing the remote manifest will fail
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: ioutil.NopCloser(bytes.NewReader([]byte(`
releases:
  2.0.0:
    stork: stork/image:2.0.0
`))),
		}, nil
	}
	readManifest = func(_ string) ([]byte, error) {
		return nil, fmt.Errorf("read error")
	}

	r, err := NewReleaseManifest()
	require.Error(t, err)
	require.Nil(t, r)
}

func TestFailureToReadManifestFromResponse(t *testing.T) {
	defer unmaskLoadManifest()
	defer setupHTTPFailure()

	// As the manifest directory is not setup, writing the remote manifest will fail
	httpGet = func(url string) (*http.Response, error) {
		return &http.Response{
			Body: ioutil.NopCloser(&failedReader{}),
		}, nil
	}
	readManifest = func(_ string) ([]byte, error) {
		return nil, fmt.Errorf("read error")
	}

	r, err := NewReleaseManifest()
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
	os.Setenv(EnvKeyReleaseManifestURL, customURL)
	httpGet = func(url string) (*http.Response, error) {
		require.Equal(t, customURL, url)
		return nil, fmt.Errorf("http error")
	}

	NewReleaseManifest()

	// Use default url if no env variable set
	os.Unsetenv(EnvKeyReleaseManifestURL)
	httpGet = func(url string) (*http.Response, error) {
		require.Equal(t, defaultReleaseManifestURL, url)
		return nil, fmt.Errorf("http error")
	}

	NewReleaseManifest()
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
	os.RemoveAll(ManifestDir)
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
