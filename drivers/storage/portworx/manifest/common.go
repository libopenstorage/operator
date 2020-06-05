package manifest

import (
	"io/ioutil"
	"net/http"
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
