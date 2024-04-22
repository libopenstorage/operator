package kubernetes

import (
	"context"
	"fmt"

	semver "github.com/Masterminds/semver/v3"

	k8schk "github.com/libopenstorage/operator/px/px-health-check/pkg/checks/kubernetes"
	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"
)

func KubernetesMinVersion() *healthcheck.Checker {
	return &healthcheck.Checker{
		Description: fmt.Sprintf("kubernetes minimum version must be %s", KubernetsMinVersionConstraint),
		HintAnchor:  "pxe/1002",
		Warning:     true,
		Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
			return k8sVersionCheck(state, KubernetsMinVersionConstraint)
		},
	}
}

func KubernetesMaxVersion() *healthcheck.Checker {
	return &healthcheck.Checker{
		Description: fmt.Sprintf("kubernetes maximum version should be %s", KubernetsMaxVersionConstraint),
		HintAnchor:  "pxe/1003",
		Warning:     true,
		Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
			return k8sVersionCheck(state, KubernetsMaxVersionConstraint)
		},
	}
}

func k8sVersionCheck(state *healthcheck.HealthCheckState, minver string) error {
	k8sv, ok := k8schk.GetKubernetesVersion(state)
	if !ok {
		return fmt.Errorf("unable to get kubernetes version")
	}
	v, err := semver.NewVersion(k8sv.String())
	if err != nil {
		return err
	}

	constraints, err := semver.NewConstraint(minver)
	if err != nil {
		return err
	}
	if !constraints.Check(v) {
		return fmt.Errorf(
			"kubernetes version of %s does not satisfy the required version of %s",
			k8sv.String(),
			minver,
		)
	}
	return nil
}
