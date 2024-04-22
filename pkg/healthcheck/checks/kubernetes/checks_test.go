package kubernetes

import (
	"testing"

	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"
	"github.com/stretchr/testify/assert"
)

// TestCheckUrlHits confirms health check
// hint URLs exist on https://docs.portworx.com
func TestCheckUrlHints(t *testing.T) {
	cats := []*healthcheck.Category{
		KubernetesHealthPreChecks(),
	}
	assert.NoError(t, healthcheck.TestHintsInHealthCheckers(cats))
}
