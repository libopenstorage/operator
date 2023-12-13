package manifest

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

const (
	httpPrefix    = "http://"
	httpsPrefix   = "https://"
	secretName    = "custom-ca-cert"
	secretKeyName = "rootCA.crt"
)

// Methods to override for testing
var (
	httpGet = http.Get
	CAfiles []string
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

		m := Instance()
		caCert, err := m.GetCACert(secretName, secretKeyName)
		if err != nil {
			logrus.Errorf("Can't load CA certificate due to: %v", err)
		}
		caCertPool := x509.NewCertPool()
		ok := caCertPool.AppendCertsFromPEM(caCert)
		var tlsConfig *tls.Config
		if ok {
			tlsConfig = &tls.Config{RootCAs: caCertPool}
		}

		client := &http.Client{
			Transport: &http.Transport{
				Proxy:           http.ProxyURL(proxyURL),
				TLSClientConfig: tlsConfig,
			},
		}

		resp, err = client.Get(manifestURL)
		if err != nil {
			return nil, err
		}
	}

	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
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
