package manifest

import (
	"io/ioutil"
	"net/http"

	yaml "gopkg.in/yaml.v2"
)

// Methods to override for testing
var (
	httpGet = http.Get
)

func getManifestFromURL(manifestURL string) ([]byte, error) {
	resp, err := httpGet(manifestURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return ioutil.ReadAll(resp.Body)
}

func parseVersionManifest(content []byte) (*Version, error) {
	manifest := &Version{}
	err := yaml.Unmarshal(content, manifest)
	if err != nil {
		return nil, err
	}
	if manifest.PortworxVersion == "" {
		return nil, ErrReleaseNotFound
	}
	return manifest, nil
}
