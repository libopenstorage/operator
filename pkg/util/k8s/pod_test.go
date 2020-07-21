package k8s

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
)

func TestAddOrUpdateStoragePodTolerations(t *testing.T) {
	p := &v1.Pod{
		Spec: v1.PodSpec{
			NodeName: "n1",
		},
	}
	AddOrUpdateStoragePodTolerations(&p.Spec)
	require.Len(t, p.Spec.Tolerations, 7)
}
