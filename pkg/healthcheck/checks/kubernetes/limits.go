package kubernetes

var (
	MinNumberOfNodes              int    = 3
	MinMemoryHardLimit            string = "4Gi"
	MinMemorySoftLimit            string = "8Gi"
	KubernetsMinVersion           string = "1.23.0"
	KubernetsMaxVersion           string = "1.30.0"
	KubernetsMinVersionConstraint string = ">= " + KubernetsMinVersion
	KubernetsMaxVersionConstraint string = "< " + KubernetsMaxVersion

	// KernelMinVersionHardConstraint should have a semver
	// of x.y.z-p so that it matches some kernels that use dashes.
	KernelMinVersionHardConstraint      string = ">= 4.18.0-0"
	KernelMinVersionPxStoreV2Constraint string = ">= 5.0.0-0"
)
