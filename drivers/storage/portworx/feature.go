package portworx

import (
	"strconv"
)

// Feature is the enum type for different features
type Feature string

const (
	// FeatureCSI is used to indicate CSI feature
	FeatureCSI Feature = "CSI"
)

func (feature Feature) isEnabled(featureMap map[string]string) bool {
	enabled, err := strconv.ParseBool(featureMap[string(feature)])
	return err == nil && enabled
}
