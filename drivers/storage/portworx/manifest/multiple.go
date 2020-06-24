package manifest

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"sort"

	version "github.com/hashicorp/go-version"
	"github.com/sirupsen/logrus"
	yaml "gopkg.in/yaml.v2"
)

const (
	// manifestDir directory where release manifest is stored
	manifestDir = "manifests"
	// localReleaseManifest name of the embedded release manifest
	localReleaseManifest = "portworx-releases-local.yaml"
	// remoteReleaseManifest name of the remote release manifest
	remoteReleaseManifest = "portworx-releases-remote.yaml"
	// defaultReleaseManifestURL is the URL to download the latest release manifest
	defaultReleaseManifestURL = "https://install.portworx.com/versions"
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
	readManifest = readReleaseManifest
)

// releaseManifest defines the Portworx release manifest
type releaseManifest struct {
	DefaultRelease string             `yaml:"defaultRelease,omitempty"`
	Releases       map[string]Release `yaml:"releases,omitempty"`
}

// DEPRECATED: This type of manifest contains component versions
// of all portworx release until 2.6.0. It is a single manifest
// with multiple releases in one. After most of the users have
// moved past 2.6.0, we will removing this method of getting
// manifest entirely.
type multiple struct {
	pxVersion string
}

func newDeprecatedManifest(pxVersion string) versionProvider {
	logrus.Debugf("Using old release manifest")
	return &multiple{
		pxVersion: pxVersion,
	}
}

func (m *multiple) Get() (*Version, error) {
	relManifest, err := fetchCompleteManifest()
	if err != nil {
		return nil, err
	}

	rel, err := relManifest.get(m.pxVersion)
	if err != nil {
		return nil, err
	}

	return &Version{
		PortworxVersion: m.pxVersion,
		Components:      *rel,
	}, nil
}

func fetchCompleteManifest() (*releaseManifest, error) {
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

// get returns a release object for given string version
func (m *releaseManifest) get(target string) (*Release, error) {
	if release, exists := m.Releases[target]; exists {
		targetRelease := release
		return &targetRelease, nil
	}
	targetVersion, err := version.NewSemver(target)
	if err != nil {
		return nil, fmt.Errorf("invalid version %s: %v", target, err)
	}
	return m.getFromVersion(targetVersion)
}

// getFromVersion returns a release object for given version
func (m *releaseManifest) getFromVersion(target *version.Version) (*Release, error) {
	for release := range m.Releases {
		curr, err := version.NewSemver(release)
		if err == nil && curr.Equal(target) {
			targetRelease := m.Releases[release]
			return &targetRelease, nil
		}
	}
	return nil, ErrReleaseNotFound
}

func (m *releaseManifest) getLatest() string {
	versions := make([]string, 0)
	for version := range m.Releases {
		versions = append(versions, version)
	}

	sort.Sort(semver(versions))
	return versions[len(versions)-1]
}

func loadLocalManifest() (*releaseManifest, error) {
	manifestPath := manifestFilepath(localReleaseManifest)
	return loadManifestFromFile(manifestPath)
}

func loadRemoteManifest() (*releaseManifest, error) {
	logrus.Debugf("Downloading latest release manifest.")
	manifest, err := downloadManifest()
	if err != nil {
		// If download fails return the existing remote manifest if it exists
		logrus.Debugf("Failed to get latest release manifest. %v", err)
		manifestPath := manifestFilepath(remoteReleaseManifest)
		return loadManifestFromFile(manifestPath)
	}
	return manifest, nil
}

func downloadManifest() (*releaseManifest, error) {
	manifestURL := defaultReleaseManifestURL
	if url, exists := os.LookupEnv(envKeyReleaseManifestURL); exists {
		manifestURL = url
	}

	body, err := getManifestFromURL(manifestURL)
	if err != nil {
		return nil, err
	}

	manifest, err := loadManifest(body)
	if err != nil {
		return nil, err
	}

	// Write the manifest for future use instead of downloading it every time
	manifestPath := manifestFilepath(remoteReleaseManifest)
	err = ioutil.WriteFile(manifestPath, body, 0644)
	if err != nil {
		return nil, err
	}

	return manifest, nil
}

func loadManifestFromFile(filename string) (*releaseManifest, error) {
	content, err := readManifest(filename)
	if err != nil {
		return nil, err
	}
	return loadManifest(content)
}

func readReleaseManifest(filename string) ([]byte, error) {
	return ioutil.ReadFile(filename)
}

func loadManifest(content []byte) (*releaseManifest, error) {
	manifest := &releaseManifest{}
	err := yaml.Unmarshal(content, manifest)
	if err != nil {
		return nil, err
	}
	return manifest, nil
}

func manifestFilepath(filename string) string {
	return path.Join(manifestDir, filename)
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
