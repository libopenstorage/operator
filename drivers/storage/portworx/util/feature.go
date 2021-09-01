package util

import (
	"strconv"
)

// Feature is the enum type for different features
type Feature string

const (
	// FeatureCSI is used to indicate CSI feature
	FeatureCSI Feature = "CSI"
)

// IsEnabled checks if the feature is enabled in the given feature map
func (feature Feature) IsEnabled(featureMap map[string]string) bool {
	enabled, err := strconv.ParseBool(featureMap[string(feature)])
	return err == nil && enabled
}
