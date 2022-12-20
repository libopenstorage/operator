package utils

import (
	"time"

	"github.com/hashicorp/go-version"
)

// Global test parameters that are set at the beginning of the run
var (
	//PxDockerUsername docker username for internal repo
	PxDockerUsername string
	// PxDockerPassword docker credential for internal repo
	PxDockerPassword string

	//PxVsphereUsername vSpehre username for authentication
	PxVsphereUsername string
	// PxVspherePassword vSphere password for authentication
	PxVspherePassword string

	// PxSpecGenURL spec url to get component images
	PxSpecGenURL string
	// PxImageOverride overrides the spec gen url passed in
	PxImageOverride string
	// PxSpecImages contains images parsed from spec gen url
	PxSpecImages map[string]string

	// PxUpgradeHopsURLList URL list for upgrade PX test
	PxUpgradeHopsURLList []string

	// OperatorUpgradeHopsImageList image list for upgrade PX Operator test
	OperatorUpgradeHopsImageList []string

	// K8sVersion is a K8s version from cluster server side
	K8sVersion string

	// PxOperatorVersion is the version of installed px operator found
	PxOperatorVersion *version.Version

	// PxOperatorTag is the version of px operator that will be used for Openshift Marketplace deployment
	PxOperatorTag string

	// PxDeviceSpecs is a list of `;` separated Portworx device specs
	PxDeviceSpecs string

	// PxKvdbSpec is a Portworx KVDB device spec
	PxKvdbSpec string

	// PxEnvVars is a string of comma separated ENV vars
	PxEnvVars string

	// CloudProvider is a cloud provider name
	CloudProvider string

	// IsOcp is an indication that this is OCP cluster or not
	IsOcp bool

	// IsEks is an indication that this is EKS cluster or not
	IsEks bool

	// IsAks is an indication that this is AKS cluster or not
	IsAks bool

	// IsGke is an indication that this is GKE cluster or not
	IsGke bool

	// IsOke is an indication that this is OKE cluster or not
	IsOke bool
)

const (
	// DefaultValidateDeployTimeout is a default timeout for deployment validation
	DefaultValidateDeployTimeout = 15 * time.Minute
	// DefaultValidateDeployRetryInterval is a default retry interval for deployment validation
	DefaultValidateDeployRetryInterval = 15 * time.Second
	// DefaultValidateUpgradeTimeout is a default timeout for upgrade validation
	DefaultValidateUpgradeTimeout = 30 * time.Minute
	// DefaultValidateUpgradeRetryInterval is a default retry interval for upgrade validation
	DefaultValidateUpgradeRetryInterval = 15 * time.Second
	// DefaultValidateUpdateTimeout is a default timeout for update validation
	DefaultValidateUpdateTimeout = 20 * time.Minute
	// DefaultValidateUpdateRetryInterval is a default retry interval for update validation
	DefaultValidateUpdateRetryInterval = 15 * time.Second
	// DefaultValidateUninstallTimeout is a default timeout for uninstall validation
	DefaultValidateUninstallTimeout = 15 * time.Minute
	// DefaultValidateUninstallRetryInterval is a default retry interval for uninstall validation
	DefaultValidateUninstallRetryInterval = 15 * time.Second

	// DefaultValidateComponentTimeout is a default timeout for component validation
	DefaultValidateComponentTimeout = 10 * time.Minute
	// DefaultValidateComponentRetryInterval is a default retry interval for component validation
	DefaultValidateComponentRetryInterval = 5 * time.Second
	// DefaultValidateApplicationTimeout is a default timeout for component validation
	DefaultValidateApplicationTimeout = 5 * time.Minute
	// DefaultValidateApplicationRetryInterval is a default retry interval for component validation
	DefaultValidateApplicationRetryInterval = 5 * time.Second

	// LabelValueTrue value "true" for a label
	LabelValueTrue = "true"
	// LabelValueFalse value "false" for a label
	LabelValueFalse = "false"

	// SourceConfigSecretName is the name of the secret that contains the superset of all credentials
	// we may select from for these tests.
	SourceConfigSecretName = "px-pure-secret-source"
	// OutputSecretName is the name of the secret we will output chosen credential subsets to.
	OutputSecretName = "px-pure-secret"

	// NodeReplacePrefix is used for replacing node name during the test
	NodeReplacePrefix = "replaceWithNodeNumber"

	// PxNamespace is a default namespace for StorageCluster
	PxNamespace = "kube-system"
	// PortworxOperatorDeploymentName name of portworx operator deployment
	PortworxOperatorDeploymentName = "portworx-operator"
	// PortworxOperatorContainerName name of portworx operator container
	PortworxOperatorContainerName = "portworx-operator"
)
