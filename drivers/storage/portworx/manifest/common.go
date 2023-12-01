package manifest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
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

		caCert, err := GetCACert(secretName, secretKeyName)
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)

		client := &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
				TLSClientConfig: &tls.Config{
					RootCAs: caCertPool,
				},
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

// read ca file from kubernetes secret using k8s client
func (m *manifest) GetCACert(
	secretName,
	secretKey string,
) ([]byte, error) {
	ctx := context.Background()
	secret := v1.Secret{}
	err := m.k8sClient.Get(ctx,
		types.NamespacedName{
			Name: secretName,
		},
		&secret,
	)
	if err != nil {
		logrus.Debugf("Can't load CA certificate due to: %v", err)
		return nil, err
	}
	return secret.Data[secretKey], nil
}
