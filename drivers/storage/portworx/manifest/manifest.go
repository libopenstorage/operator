package manifest

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"sort"
	"time"

	version "github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	yaml "gopkg.in/yaml.v2"
)

const (
	// ManifestDir directory where release manifest is stored
	ManifestDir = "manifests"
	// LocalReleaseManifest name of the embedded release manifest
	LocalReleaseManifest = "portworx-releases-local.yaml"
	// RemoteReleaseManifest name of the remote release manifest
	RemoteReleaseManifest = "portworx-releases-remote.yaml"
	// EnvKeyReleaseManifestURL is an environment variable to override the
	// default release manifest download URL
	EnvKeyReleaseManifestURL = "PX_RELEASE_MANIFEST_URL"
	// defaultReleaseManifestURL is the URL to download the latest release manifest
	defaultReleaseManifestURL = "https://install.portworx.com/versions"
	// defaultManifestRefreshInternal internal after which we should refresh the
	// downloaded release manifest
	defaultManifestRefreshInterval = time.Hour
)

var (
	// ErrEmptyReleases when releases list is empty
	ErrEmptyReleases = errors.New("releases cannot be empty")
	// ErrReleaseNotFound when given release is not found
	ErrReleaseNotFound = errors.New("release not found")
	// ErrInvalidDefaultRelease when the default release is invalid
	ErrInvalidDefaultRelease = errors.New("invalid default release")
)

// Methods to override for testing
var (
	readManifest    = readReleaseManifest
	refreshInterval = manifestRefreshInterval
	httpGet         = http.Get
)

// ReleaseManifest defines the Portworx release manifest
type ReleaseManifest struct {
	DefaultRelease string             `yaml:"defaultRelease,omitempty"`
	Releases       map[string]Release `yaml:"releases,omitempty"`
}

// Release is a single release object with images for different components
type Release struct {
	Stork      string `yaml:"stork,omitempty"`
	Lighthouse string `yaml:"lighthouse,omitempty"`
	Autopilot  string `yaml:"autopilot,omitempty"`
}

// NewReleaseManifest returns a release manifest object from the portworx releases file
func NewReleaseManifest() (*ReleaseManifest, error) {
	manifest, err := loadRemoteManifest()
	if err != nil {
		logrus.Debugf("Using local release manifest as loading remote manifest failed due to: %v", err)
		manifest, err = loadLocalManifest()
		if err != nil {
			return nil, err
		}
	}

	if len(manifest.Releases) == 0 {
		return nil, ErrEmptyReleases
	}
	if len(manifest.DefaultRelease) == 0 {
		manifest.DefaultRelease = manifest.getLatest()
	} else if _, exists := manifest.Releases[manifest.DefaultRelease]; !exists {
		return nil, ErrInvalidDefaultRelease
	}
	return manifest, nil
}

// Get returns a release object for given string version
func (m *ReleaseManifest) Get(target string) (*Release, error) {
	if release, exists := m.Releases[target]; exists {
		targetRelease := release
		return &targetRelease, nil
	}
	targetVersion, err := version.NewSemver(target)
	if err != nil {
		return nil, fmt.Errorf("invalid version %s: %v", target, err)
	}
	return m.GetFromVersion(targetVersion)
}

// GetFromVersion returns a release object for given version
func (m *ReleaseManifest) GetFromVersion(target *version.Version) (*Release, error) {
	if target == nil {
		return nil, ErrReleaseNotFound
	}
	for release := range m.Releases {
		curr, err := version.NewSemver(release)
		if err == nil && curr.Equal(target) {
			targetRelease := m.Releases[release]
			return &targetRelease, nil
		}
	}
	return nil, ErrReleaseNotFound
}

// GetDefault returns a release object for the default release
func (m *ReleaseManifest) GetDefault() (*Release, error) {
	return m.Get(m.DefaultRelease)
}

func (m *ReleaseManifest) getLatest() string {
	versions := make([]string, 0)
	for version := range m.Releases {
		versions = append(versions, version)
	}

	sort.Sort(semver(versions))
	return versions[len(versions)-1]
}

func loadLocalManifest() (*ReleaseManifest, error) {
	manifestPath := manifestFilepath(LocalReleaseManifest)
	return loadManifestFromFile(manifestPath)
}

func loadRemoteManifest() (*ReleaseManifest, error) {
	var fileExists bool
	manifestPath := manifestFilepath(RemoteReleaseManifest)
	file, err := os.Stat(manifestPath)
	if err != nil {
		logrus.Debugf("Cannot read release manifest. %v", err)
	} else {
		fileExists = true
		expirationTime := time.Now().Add(-refreshInterval())
		if file.ModTime().Before(expirationTime) {
			logrus.Debugf("Release manifest is stale.")
		} else {
			manifest, err := loadManifestFromFile(manifestPath)
			if err == nil {
				return manifest, nil
			}
			logrus.Debugf("Failed to load existing release manifest. %v", err)
		}
	}

	logrus.Debugf("Downloading latest release manifest.")
	manifest, err := downloadManifest()
	if err != nil {
		logrus.Debugf("Failed to get latest release manifest. %v", err)
		if fileExists {
			// If download fails return the existing remote manifest if it exists
			return loadManifestFromFile(manifestPath)
		}
		return nil, err
	}
	return manifest, nil
}

func downloadManifest() (*ReleaseManifest, error) {
	manifestURL := defaultReleaseManifestURL
	if url, exists := os.LookupEnv(EnvKeyReleaseManifestURL); exists {
		manifestURL = url
	}
	resp, err := httpGet(manifestURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	manifest, err := loadManifest(body)
	if err != nil {
		return nil, err
	}

	// Write the manifest for future use instead of downloading it every time
	manifestPath := manifestFilepath(RemoteReleaseManifest)
	err = ioutil.WriteFile(manifestPath, body, 0644)
	if err != nil {
		return nil, err
	}

	return manifest, nil
}

func loadManifestFromFile(filename string) (*ReleaseManifest, error) {
	content, err := readManifest(filename)
	if err != nil {
		return nil, err
	}
	return loadManifest(content)
}

func readReleaseManifest(filename string) ([]byte, error) {
	return ioutil.ReadFile(filename)
}

func loadManifest(content []byte) (*ReleaseManifest, error) {
	manifest := &ReleaseManifest{}
	err := yaml.Unmarshal(content, manifest)
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

func manifestFilepath(filename string) string {
	return path.Join(ManifestDir, filename)
}

func manifestRefreshInterval() time.Duration {
	return defaultManifestRefreshInterval
}

type semver []string

func (s semver) Len() int {
	return len(s)
}

func (s semver) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

// The versions are compared on follow criteria:
// - if both are semver, return semver comparision result
// - if both are not semver, return lexicographic comparison result
// - if one is semver and other is not, then semver is considered smaller
func (s semver) Less(i, j int) bool {
	version1, err1 := version.NewSemver(s[i])
	version2, err2 := version.NewSemver(s[j])
	if err1 == nil && err2 == nil {
		return version1.LessThan(version2)
	} else if err1 != nil && err2 != nil {
		return s[i] < s[j]
	}
	return err1 == nil
}
