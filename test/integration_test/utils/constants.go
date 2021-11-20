package utils

import "time"

// Global test parameters that are set at the beginning of the run
var (
	//PxDockerUsername docker user name for internal repo
	PxDockerUsername string
	// PxDockerPassword docker credential for internal repo
	PxDockerPassword string

	// PxSpecGenURL spec url to get component images
	PxSpecGenURL string
	// PxImageOverride overrides the spec gen url passed in
	PxImageOverride string
	// PxSpecImages contains images parsed from spec gen url
	PxSpecImages map[string]string

	// PxUpgradeHopsURLList urls for upgrade test
	PxUpgradeHopsURLList []string

	// K8sVersion is a K8s version from cluster server side
	K8sVersion string
)

const (
	// DefaultValidateDeployTimeout is a default timeout for deployment validation
	DefaultValidateDeployTimeout = 15 * time.Minute
	// DefaultValidateDeployRetryInterval is a default retry interval for deployment validation
	DefaultValidateDeployRetryInterval = 20 * time.Second
	// DefaultValidateUpgradeTimeout is a default timeout for upgrade validation
	DefaultValidateUpgradeTimeout = 25 * time.Minute
	// DefaultValidateUpgradeRetryInterval is a default retry interval for upgrade validation
	DefaultValidateUpgradeRetryInterval = 20 * time.Second
	// DefaultValidateUpdateTimeout is a default timeout for update validation
	DefaultValidateUpdateTimeout = 20 * time.Minute
	// DefaultValidateUpdateRetryInterval is a default retry interval for update validation
	DefaultValidateUpdateRetryInterval = 20 * time.Second
	// DefaultValidateUninstallTimeout is a default timeout for uninstall validation
	DefaultValidateUninstallTimeout = 15 * time.Minute
	// DefaultValidateUninstallRetryInterval is a default retry interval for uninstall validation
	DefaultValidateUninstallRetryInterval = 20 * time.Second
	// DefaultValidateStorkTimeout is a default timeout for stork validation
	DefaultValidateStorkTimeout = 10 * time.Minute
	// DefaultValidateStorkRetryInterval is a default retry interval for stork validation
	DefaultValidateStorkRetryInterval = 5 * time.Second

	// SourceConfigSecretName is the name of the secret that contains the superset of all credentials
	// we may select from for these tests.
	SourceConfigSecretName = "px-pure-secret-source"
	// OutputSecretName is the name of the secret we will output chosen credential subsets to.
	OutputSecretName = "px-pure-secret"

	// NodeReplacePrefix is used for replacing node name during the test
	NodeReplacePrefix = "replaceWithNodeNumber"

	// PortworxOperatorDeploymentName name of portworx operator deployment
	PortworxOperatorDeploymentName = "portworx-operator"
)
