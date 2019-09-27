package manifest

import (
	"errors"
	"fmt"
	"io/ioutil"
	"path"
	"sort"

	version "github.com/hashicorp/go-version"
	yaml "gopkg.in/yaml.v2"
)

const (
	// ManifestDir directory where release manifest is stored
	ManifestDir = "manifests"
	// ReleasesFilename name of the release manifest file
	ReleasesFilename = "portworx-releases.yaml"
)

var (
	// ErrEmptyReleases when releases list is empty
	ErrEmptyReleases = errors.New("releases cannot be empty")
	// ErrReleaseNotFound when given release is not found
	ErrReleaseNotFound = errors.New("release not found")
	// ErrInvalidDefaultRelease when the default release is invalid
	ErrInvalidDefaultRelease = errors.New("invalid default release")
	loadReleaseManifest      = readManifestFile
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
}

// NewReleaseManifest returns a release manifest object from the portworx releases file
func NewReleaseManifest() (*ReleaseManifest, error) {
	data, err := loadReleaseManifest()
	if err != nil {
		return nil, err
	}
	manifest := &ReleaseManifest{}
	err = yaml.Unmarshal([]byte(data), manifest)
	if err != nil {
		return nil, err
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

func readManifestFile() ([]byte, error) {
	return ioutil.ReadFile(path.Join(ManifestDir, ReleasesFilename))
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
