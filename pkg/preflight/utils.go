package preflight

import (
	"github.com/libopenstorage/cloudops"
)

// IsEKS returns whether the cloud environment is running EKS
func IsEKS() bool {
	return Instance().ProviderName() == string(cloudops.AWS) && Instance().K8sDistributionName() == eksDistribution
}

// RequiresCheck returns whether a preflight check is needed based on the platform
func RequiresCheck() bool {
	// TODO: add other scenarios here
	return IsEKS()
}

// RunningOnCloud checks whether portworx is running on cloud
func RunningOnCloud() bool {
	// TODO: add other clouds
	return IsEKS()
}
