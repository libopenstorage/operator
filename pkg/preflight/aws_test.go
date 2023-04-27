package preflight

import (
	"crypto/md5"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"

	"github.com/libopenstorage/cloudops"
	"github.com/libopenstorage/cloudops/mock"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
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
	err := InitPreflightChecker(testutil.FakeK8sClient())
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
	err := InitPreflightChecker(testutil.FakeK8sClient())
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

func TestSetAWSCredentialEnvVars(t *testing.T) {
	defer unsetAWSCredentialEnvVars()

	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))
	err := InitPreflightChecker(testutil.FakeK8sClient())
	require.NoError(t, err)
	awsChecker := &aws{}
	SetInstance(awsChecker)

	expectedAccessKey := "accesskey"
	expectedSecretKey := "secretkey"
	expectedHash := fmt.Sprintf("%x", md5.Sum([]byte(expectedAccessKey+expectedSecretKey)))
	cluster := &corev1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1.StorageClusterSpec{
			CommonConfig: corev1.CommonConfig{
				Env: []v1.EnvVar{
					{
						Name:  awsAccessKeyEnvName,
						Value: expectedAccessKey,
					},
					{
						Name:  awsSecretKeyEnvName,
						Value: expectedSecretKey,
					},
				},
			},
		},
	}

	// TestCase: both access and secret key are specified in the stc env
	require.NoError(t, awsChecker.setAWSCredentialEnvVars(cluster))
	require.Equal(t, expectedAccessKey, os.Getenv(awsAccessKeyEnvName))
	require.Equal(t, expectedSecretKey, os.Getenv(awsSecretKeyEnvName))
	require.Equal(t, expectedHash, awsChecker.credentialHash)

	// TestCase: reset env vars on errors
	require.Error(t, Instance().CheckCloudDrivePermission(cluster), "expected error setting up aws client")
	require.Empty(t, os.Getenv(awsAccessKeyEnvName))
	require.Empty(t, os.Getenv(awsSecretKeyEnvName))

	// TestCase: set env vars when credentials are updated
	newAccessKey := "newaccesskey"
	newSecretKey := "newsecretkey"
	newHash := fmt.Sprintf("%x", md5.Sum([]byte(newAccessKey+newSecretKey)))
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name:  awsAccessKeyEnvName,
			Value: newAccessKey,
		},
		{
			Name:  awsSecretKeyEnvName,
			Value: newSecretKey,
		},
	}
	require.NoError(t, awsChecker.setAWSCredentialEnvVars(cluster))
	require.Equal(t, newAccessKey, os.Getenv(awsAccessKeyEnvName))
	require.Equal(t, newSecretKey, os.Getenv(awsSecretKeyEnvName))
	require.Equal(t, newHash, awsChecker.credentialHash)

	// TestCase: credentials are removed
	cluster.Spec.Env = nil
	require.NoError(t, awsChecker.setAWSCredentialEnvVars(cluster))
	require.Empty(t, os.Getenv(awsAccessKeyEnvName))
	require.Empty(t, os.Getenv(awsSecretKeyEnvName))
	require.Empty(t, awsChecker.credentialHash)

	// TestCase: only one key provided in the stc env
	cluster.Spec.Env = []v1.EnvVar{{
		Name:  awsAccessKeyEnvName,
		Value: expectedAccessKey,
	}}
	err = awsChecker.setAWSCredentialEnvVars(cluster)
	require.Error(t, err)
	require.ErrorContains(t, err, "both AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY need to be provided")
	require.Empty(t, os.Getenv(awsAccessKeyEnvName))
	require.Empty(t, os.Getenv(awsSecretKeyEnvName))
	require.Empty(t, awsChecker.credentialHash)

	// TestCase: setup env vars from secret
	cluster.Spec.Env = []v1.EnvVar{
		{
			Name: awsAccessKeyEnvName,
			ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					Key: "access-key",
					LocalObjectReference: v1.LocalObjectReference{
						Name: "aws-secret",
					},
				},
			},
		},
		{
			Name: awsSecretKeyEnvName,
			ValueFrom: &v1.EnvVarSource{
				SecretKeyRef: &v1.SecretKeySelector{
					Key: "secret-key",
					LocalObjectReference: v1.LocalObjectReference{
						Name: "aws-secret",
					},
				},
			},
		},
	}
	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "aws-secret",
			Namespace: cluster.Namespace,
		},
		Data: map[string][]byte{
			"access-key": []byte(expectedAccessKey),
			"secret-key": []byte(expectedSecretKey),
		},
	}
	k8sClient := testutil.FakeK8sClient(secret)
	awsChecker.k8sClient = k8sClient
	require.NoError(t, awsChecker.setAWSCredentialEnvVars(cluster))
	require.Equal(t, expectedAccessKey, os.Getenv(awsAccessKeyEnvName))
	require.Equal(t, expectedSecretKey, os.Getenv(awsSecretKeyEnvName))
	require.Equal(t, expectedHash, awsChecker.credentialHash)
}

func stringPtr(val string) *string {
	return &val
}
