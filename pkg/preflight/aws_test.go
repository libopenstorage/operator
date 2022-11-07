package preflight

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/libopenstorage/cloudops"
	"github.com/libopenstorage/cloudops/mock"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
)

func TestEKSCloudPermissionPassed(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
	}

	volume := &ec2.Volume{
		VolumeId: stringPtr("vol-03227127b9b7b442e"),
		Attachments: []*ec2.VolumeAttachment{
			{
				Device: stringPtr("/dev/xvdp"),
				State:  stringPtr("attached"),
			},
		},
	}
	volumeList := make([]interface{}, 1)
	volumeList[0] = volume

	fakeAWSOps := mock.NewMockOps(mockCtrl)
	fakeAWSOps.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).Return(volumeList[0], nil)
	errMsg := fmt.Sprintf("unable to map volume %s with block device mapping %s to an actual device path on the host", *volume.VolumeId, *volume.Attachments[0].State)
	fakeAWSOps.EXPECT().Attach(gomock.Any(), gomock.Any()).Return("", errors.New(errMsg)).Times(1)
	fakeAWSOps.EXPECT().Inspect(gomock.Any(), gomock.Any()).Return(volumeList, nil).Times(1)
	fakeAWSOps.EXPECT().Detach(gomock.Any(), gomock.Any()).Return(nil).Times(1)
	fakeAWSOps.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil).Times(1)
	fakeAWSOps.EXPECT().Enumerate(nil, gomock.Any(), "").Return(map[string][]interface{}{}, nil).Times(1)

	err := InitPreflightChecker()
	require.NoError(t, err)
	SetInstance(&aws{ops: fakeAWSOps})
	require.NoError(t, Instance().CheckCloudDrivePermission(cluster))
}

func TestEKSCloudPermissionRetry(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
	}

	// Failed to attach volume 1
	volume1 := &ec2.Volume{
		VolumeId: stringPtr("vol-id-1"),
	}
	volumeList := make([]interface{}, 1)
	volumeList[0] = volume1

	fakeAWSOps := mock.NewMockOps(mockCtrl)
	fakeAWSOps.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).Return(volumeList[0], nil)
	fakeAWSOps.EXPECT().Attach(gomock.Any(), gomock.Any()).Return("", fmt.Errorf("failed to attach volume"))

	err := InitPreflightChecker()
	require.NoError(t, err)
	SetInstance(&aws{ops: fakeAWSOps})
	require.Error(t, Instance().CheckCloudDrivePermission(cluster))

	// Preflight check for volume 2 passed, volume 1 should be deleted as well
	volume2 := &ec2.Volume{
		VolumeId: stringPtr("vol-id-2"),
		Attachments: []*ec2.VolumeAttachment{
			{
				Device: stringPtr("/dev/xvdp"),
				State:  stringPtr("attached"),
			},
		},
	}
	volumeList[0] = volume2

	fakeAWSOps.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).Return(volumeList[0], nil)
	errMsg := fmt.Sprintf("unable to map volume %s with block device mapping %s to an actual device path on the host", *volume2.VolumeId, *volume2.Attachments[0].State)
	fakeAWSOps.EXPECT().Attach(gomock.Any(), gomock.Any()).Return("", errors.New(errMsg))
	fakeAWSOps.EXPECT().Inspect(gomock.Any(), gomock.Any()).Return(volumeList, nil)
	fakeAWSOps.EXPECT().Enumerate(nil, gomock.Any(), "").Return(map[string][]interface{}{
		cloudops.SetIdentifierNone: {volume1},
	}, nil).Times(1)
	fakeAWSOps.EXPECT().Detach(gomock.Any(), gomock.Any()).Return(nil).Times(2)
	fakeAWSOps.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil).Times(2)

	require.NoError(t, Instance().CheckCloudDrivePermission(cluster))
}

func stringPtr(val string) *string {
	return &val
}
