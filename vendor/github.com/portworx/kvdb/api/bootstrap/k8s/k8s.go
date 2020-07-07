package k8s

import (
	"regexp"
	"strings"
)

var (
	configMapNameRegex = regexp.MustCompile("[^a-zA-Z0-9]+")
)

// GetBootstrapConfigMapName returns the name of the config map used to bootstrap a PX kvdb cluster with the given
// clusterID
func GetBootstrapConfigMapName(clusterID string) string {
	return "px-bootstrap-" + strings.ToLower(configMapNameRegex.ReplaceAllString(clusterID, ""))
}
