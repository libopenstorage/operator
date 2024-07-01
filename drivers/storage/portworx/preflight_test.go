package portworx

import (
	"fmt"
	"testing"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
)

func TestDmthinFacdDefCmeta(t *testing.T) {
	fakeRecorder := record.NewFakeRecorder(10)

	u := &preFlightPortworx{
		cluster: &corev1.StorageCluster{
			Spec: corev1.StorageClusterSpec{
				CommonConfig: corev1.CommonConfig{
					Env: []v1.EnvVar{
						{
							Name:  "PURE_FLASHARRAY_SAN_TYPE",
							Value: "PURE_FLASHARRAY_SAN_TYPE",
						},
					},
				},
				CloudStorage: &corev1.CloudStorageSpec{
					CloudStorageCommon: corev1.CloudStorageCommon{
						DeviceSpecs: &[]string{"size=49,pod=testpod"},
					},
				},
			},
		},
		hardFail: true,
	}
	storageNodes := []*corev1.StorageNode{
		{
			Status: corev1.NodeStatus{
				Checks: []corev1.CheckResult{
					{
						Type: "status",
					},
				},
			},
		},
	}
	err := u.ProcessPreFlightResults(fakeRecorder, storageNodes)
	require.Nil(t, err)
	require.Equal(t, fmt.Sprintf("%s,%s", DefCmetaFACD, "pod=testpod"), *u.cluster.Spec.CloudStorage.SystemMdDeviceSpec)
}
