package kubernetes

import (
	"context"
	"fmt"

	semver "github.com/Masterminds/semver/v3"

	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"
	"github.com/libopenstorage/operator/px/px-health-check/pkg/util"
)

func K8sKernelMinVersionHardLimit() *healthcheck.Checker {
	return &healthcheck.Checker{
		Description: fmt.Sprintf("kernel version must be %s", KernelMinVersionHardConstraint),
		HintAnchor:  "pxe/1007",
		Warning:     true,
		Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
			return checkKernelVersion(state, KernelMinVersionHardConstraint)
		},
	}
}

func K8sKernelMinPxStorageV2VersionLimit() *healthcheck.Checker {
	return &healthcheck.Checker{
		Description: fmt.Sprintf("kernel version should be %s for Px-StorageV2 support", KernelMinVersionPxStoreV2Constraint),
		HintAnchor:  "pxe/1008",
		Warning:     true,
		Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
			return checkKernelVersion(state, KernelMinVersionPxStoreV2Constraint)
		},
	}
}

func checkKernelVersion(state *healthcheck.HealthCheckState, limit string) error {
	nodes, ok := GetKubernetesNodesList(state)
	if !ok {
		return fmt.Errorf("unable to get kubernetes node list")
	}

	contraint, err := semver.NewConstraint(limit)
	if err != nil {
		return fmt.Errorf("bug: unable to create kernel version constraint")
	}

	var badNodes []string
	for _, node := range nodes.Items {
		v, err := semver.NewVersion(
			util.VersionToSemver(node.Status.NodeInfo.KernelVersion),
		)
		if err != nil {
			return fmt.Errorf("unable to get kernel version from node: %v", err)
		}

		if !contraint.Check(v) {
			if len(badNodes) <= 0 {
				badNodes = []string{"\n"}
			}

			badNodes = append(badNodes, fmt.Sprintf(
				"Current kernel version in %s is %s\n",
				node.GetName(),
				node.Status.NodeInfo.KernelVersion,
			))
		}
	}

	if len(badNodes) > 0 {
		return fmt.Errorf(
			"the following node(s) have kernel versions that do not satisfy this requirement: %+v",
			badNodes)
	}

	return nil
}
