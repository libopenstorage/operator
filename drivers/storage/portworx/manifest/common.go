package manifest

import (
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"

	"gopkg.in/yaml.v2"

	v1 "k8s.io/api/core/v1"
)

const (
	httpPrefix  = "http://"
	httpsPrefix = "https://"
)

// Methods to override for testing
var (
	httpGet = http.Get
)

func getManifestFromURL(manifestURL string, proxy string) ([]byte, error) {
	var resp *http.Response
	var err error
	if proxy == "" {
		resp, err = httpGet(manifestURL)
		if err != nil {
			return nil, err
		}
	} else {
		// Add the http prefix so we can parse the URL:
		// Wrong format: url.Parse("127.0.0.1:3213")
		// Correct format: url.Parse("http://127.0.0.1:3213")
		// If no http or https prefix is given, we assume it is http.
		if !strings.HasPrefix(strings.ToLower(proxy), httpPrefix) &&
			!strings.HasPrefix(strings.ToLower(proxy), httpsPrefix) {
			proxy = httpPrefix + proxy
		}

		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, err
		}

		client := &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}

		resp, err = client.Get(manifestURL)
		if err != nil {
			return nil, err
		}
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

// ParseVersionConfigMap parses version configmap
func ParseVersionConfigMap(cm *v1.ConfigMap) (*Version, error) {
	data, exists := cm.Data[VersionConfigMapKey]
	if !exists {
		// If the exact key does not exist, just take the first one
		// as only one key is expected
		for _, value := range cm.Data {
			data = value
			break
		}
	}
	return parseVersionManifest([]byte(data))
}
