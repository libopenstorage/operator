package preflight

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"

	"github.com/libopenstorage/cloudops"
	"github.com/libopenstorage/cloudops/mock"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	coreops "github.com/portworx/sched-ops/k8s/core"
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
	fakeAWSOps.EXPECT().Enumerate(nil, gomock.Any(), "").Return(nil, nil).Times(1)
	fakeAWSOps.EXPECT().Create(gomock.Any(), gomock.Any(), nil).Return(volumeList[0], nil).Times(1)
	fakeAWSOps.EXPECT().Delete(gomock.Any(), nil).Return(nil).Times(1)

	fakeAWSOps.EXPECT().Attach(gomock.Any(), dryRunOption).Return("", errors.New(dryRunErrMsg)).Times(1)
	fakeAWSOps.EXPECT().Expand(gomock.Any(), gomock.Any(), dryRunOption).Return(uint64(0), errors.New(dryRunErrMsg)).Times(1)
	fakeAWSOps.EXPECT().ApplyTags(gomock.Any(), gomock.Any(), dryRunOption).Return(errors.New(dryRunErrMsg)).Times(1)
	fakeAWSOps.EXPECT().RemoveTags(gomock.Any(), gomock.Any(), dryRunOption).Return(errors.New(dryRunErrMsg)).Times(1)
	fakeAWSOps.EXPECT().Detach(gomock.Any(), dryRunOption).Return(errors.New(dryRunErrMsg)).Times(1)
	fakeAWSOps.EXPECT().Delete(gomock.Any(), dryRunOption).Return(errors.New(dryRunErrMsg)).Times(1)

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
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

	// Failed to attach volume after creation
	volume := &ec2.Volume{
		VolumeId: stringPtr("vol-id"),
	}
	volumeList := make([]interface{}, 1)
	volumeList[0] = volume

	fakeAWSOps := mock.NewMockOps(mockCtrl)
	fakeAWSOps.EXPECT().Enumerate(nil, gomock.Any(), "").Return(nil, nil).Times(1)
	fakeAWSOps.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).Return(volumeList[0], nil).Times(1)
	fakeAWSOps.EXPECT().Attach(gomock.Any(), dryRunOption).Return("", fmt.Errorf("failed")).Times(1)

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	err := InitPreflightChecker()
	require.NoError(t, err)
	SetInstance(&aws{ops: fakeAWSOps})
	require.Error(t, Instance().CheckCloudDrivePermission(cluster))

	// Preflight check for volume passed without creation
	fakeAWSOps.EXPECT().Enumerate(nil, gomock.Any(), "").Return(map[string][]interface{}{cloudops.SetIdentifierNone: {volume}}, nil).Times(1)
	fakeAWSOps.EXPECT().Delete(gomock.Any(), nil).Return(nil).Times(1)

	fakeAWSOps.EXPECT().Attach(gomock.Any(), dryRunOption).Return("", errors.New(dryRunErrMsg)).Times(1)
	fakeAWSOps.EXPECT().Expand(gomock.Any(), gomock.Any(), dryRunOption).Return(uint64(0), errors.New(dryRunErrMsg)).Times(1)
	fakeAWSOps.EXPECT().ApplyTags(gomock.Any(), gomock.Any(), dryRunOption).Return(errors.New(dryRunErrMsg)).Times(1)
	fakeAWSOps.EXPECT().RemoveTags(gomock.Any(), gomock.Any(), dryRunOption).Return(errors.New(dryRunErrMsg)).Times(1)
	fakeAWSOps.EXPECT().Detach(gomock.Any(), dryRunOption).Return(errors.New(dryRunErrMsg)).Times(1)
	fakeAWSOps.EXPECT().Delete(gomock.Any(), dryRunOption).Return(errors.New(dryRunErrMsg)).Times(1)

	require.NoError(t, Instance().CheckCloudDrivePermission(cluster))
}

func stringPtr(val string) *string {
	return &val
}
