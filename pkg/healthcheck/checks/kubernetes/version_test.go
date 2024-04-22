package kubernetes

import (
	"context"
	"testing"

	k8schk "github.com/libopenstorage/operator/px/px-health-check/pkg/checks/kubernetes"
	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"
	common "github.com/portworx/portworxapis/sdk/golang/public/portworx/common/apiv1"
	"github.com/stretchr/testify/assert"
	k8sversion "k8s.io/apimachinery/pkg/version"
)

func TestKubernetesMinVersion(t *testing.T) {
	state := healthcheck.NewHealthCheckState()
	state.Set(k8schk.KubernetesVersionKey, &k8sversion.Info{
		GitVersion: "v1.22.0",
	})
	check := KubernetesMinVersion()
	assert.True(t, check.Warning)

	err := check.Check(context.Background(), state)
	assert.Error(t, err)

	state.Delete(k8schk.KubernetesVersionKey)
	state.Set(k8schk.KubernetesVersionKey, &k8sversion.Info{
		GitVersion: "v1.32.0",
	})
	err = check.Check(context.Background(), state)
	assert.NoError(t, err)

	state.Delete(k8schk.KubernetesVersionKey)
	assert.Error(t, check.Check(context.Background(), state))
}

func TestKubernetesMaxVersion(t *testing.T) {
	state := healthcheck.NewHealthCheckState()
	state.Set(k8schk.KubernetesVersionKey, &k8sversion.Info{
		GitVersion: "v1.28.0",
	})
	check := KubernetesMaxVersion()
	err := check.Check(context.Background(), state)
	assert.NoError(t, err)

	state.Delete(k8schk.KubernetesVersionKey)
	state.Set(k8schk.KubernetesVersionKey, &k8sversion.Info{
		GitVersion: "v1.32.0",
	})
	err = check.Check(context.Background(), state)
	assert.Error(t, err)

	state.Delete(k8schk.KubernetesVersionKey)
	assert.Error(t, check.Check(context.Background(), state))
}

func TestKubernetesMinMaxVersionComplete(t *testing.T) {
	cat := healthcheck.NewCategory(
		"test1",
		[]*healthcheck.Checker{
			{
				Description: "setup",
				HintAnchor:  "none",
				Check: func(ctx context.Context, state *healthcheck.HealthCheckState) error {
					state.Set(k8schk.KubernetesVersionKey, &k8sversion.Info{
						GitVersion: "v1.28.0",
					})
					return nil
				},
			},
			KubernetesMinVersion(),
			KubernetesMaxVersion(),
		},
		true,
		"none")
	hc := healthcheck.NewHealthChecker([]*healthcheck.Category{cat})
	results := hc.Run().ToHealthCheckResults()
	assert.True(t, results.Success)
	assert.False(t, results.Warning)
	assert.Len(t, results.Categories, 1)
	rc := results.Categories[0]
	assert.Len(t, rc.Checks, 3)
	assert.Equal(t, common.HealthCheckResultTypes_SUCCESS, rc.Checks[0].Result)
	assert.Equal(t, common.HealthCheckResultTypes_SUCCESS, rc.Checks[1].Result)
	assert.Equal(t, common.HealthCheckResultTypes_SUCCESS, rc.Checks[2].Result)
}
