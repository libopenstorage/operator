package kubernetes

import (
	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"
)

func KubernetesHealthPreChecks() *healthcheck.Category {

	checks := []*healthcheck.Checker{
		K8sMinNumNodes(),
		K8sMinMemoryHardLimit(),
		K8sMinMemorySoftLimit(),
		KubernetesMinVersion(),
		KubernetesMaxVersion(),
		K8sKernelMinVersionHardLimit(),
		K8sKernelMinPxStorageV2VersionLimit(),
	}

	return healthcheck.NewCategory(
		"kubernetes precheck",
		checks,
		true,
		healthcheck.PortworxDefaultHintBaseUrl,
	)
}
