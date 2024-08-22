package healthcheck

import (
	"testing"

	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"
	"github.com/stretchr/testify/assert"
)

func TestCheckUrlHints(t *testing.T) {
	cats := []*healthcheck.Category{
		RequiredHealthChecks(),
		GatherFactsHealthChecks(nil, nil),
	}
	assert.NoError(t, healthcheck.TestHintsInHealthCheckers(cats))
}
