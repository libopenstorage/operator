package types

import "encoding/json"

// DiscoveryConfig represents a single pure.json file
type DiscoveryConfig struct {
	Arrays []FlashArrayEntry `json:"FlashArrays,omitempty"`
	Blades []FlashBladeEntry `json:"FlashBlades,omitempty"`
}

// FlashArrayEntry represents a single FlashArray in a pure.json file
type FlashArrayEntry struct {
	APIToken     string            `json:"APIToken,omitempty"`
	MgmtEndPoint string            `json:"MgmtEndPoint,omitempty"`
	Labels       map[string]string `json:"Labels,omitempty"`
}

// FlashBladeEntry represents a single FlashBlade in a pure.json file
type FlashBladeEntry struct {
	MgmtEndPoint string            `json:"MgmtEndPoint,omitempty"`
	NFSEndPoint  string            `json:"NFSEndPoint,omitempty"`
	APIToken     string            `json:"APIToken,omitempty"`
	Labels       map[string]string `json:"Labels,omitempty"`
}

// PureBackendRequirements is used to describe what backends are required for
// a given test, including how many should be invalid.
type PureBackendRequirements struct {
	// RequiredArrays and RequiredBlades indicate how many FlashArrays and
	// FlashBlades are required respectively. If not enough are present in
	// the source secret, this test will instead be skipped. If set to
	// AllAvailableBackends, all backends of that type will be used.
	RequiredArrays, RequiredBlades int
	// InvalidArrays and InvalidBlades indicate how many FlashArrays and
	// FlashBlades respectively should have invalid credentials supplied.
	// If set to AllAvailableBackends, all backends will have their
	// credentials set invalid.
	InvalidArrays, InvalidBlades int
}

// DumpJSON returns this DiscoveryConfig in a JSON byte array,
// the correct format for use in the k8s Secret API
func (dc *DiscoveryConfig) DumpJSON() ([]byte, error) {
	return json.Marshal(*dc)
}
