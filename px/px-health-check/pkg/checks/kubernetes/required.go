package kubernetes

import (
	"context"
	"fmt"
	"time"

	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"
	"github.com/libopenstorage/operator/px/px-health-check/pkg/states"

	k8sversion "k8s.io/apimachinery/pkg/version"
)

const (
	KubernetesVersionKey healthcheck.HealthStateDataKey = "k8s/version"
)

func RequiredKubernetesVersion() *healthcheck.Checker {
	return &healthcheck.Checker{
		Description:   "get kubernetes version",
		HintAnchor:    "pxe/1000",
		Fatal:         true,
		RetryDeadline: 10 * time.Second,
		Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
			c, ok := states.GetKubernetesClientSet(state)
			if !ok {
				return fmt.Errorf("unable to get kubernetes client")
			}
			v, err := c.Discovery().ServerVersion()
			if err != nil {
				return fmt.Errorf("unable to get the version of the Kuberntes cluster: %v", err)
			}

			state.Set(KubernetesVersionKey, v)
			return nil
		},
	}
}

func GetKubernetesVersion(state *healthcheck.HealthCheckState) (*k8sversion.Info, bool) {
	if v, ok := state.Get(KubernetesVersionKey).(*k8sversion.Info); ok {
		return v, ok
	}
	return nil, false
}
