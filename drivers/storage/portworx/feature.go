package portworx

import (
	"strconv"
)

// Feature is the enum type for different features
type Feature string

const (
	// CSI is used to indicate CSI feature
	CSI Feature = "CSI"
)

func (feature Feature) isEnabled(featureMap map[string]string) bool {
	enabled, err := strconv.ParseBool(featureMap[string(feature)])
	return err == nil && enabled
}
