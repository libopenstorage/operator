package migration

import (
	"context"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	testutil "github.com/libopenstorage/operator/pkg/util/test"

	"k8s.io/apimachinery/pkg/util/wait"
)

func TestBackup(t *testing.T) {
	testBackup(t, true)
	testBackup(t, false)
}

func testBackup(t *testing.T, backupExits bool) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  "portworx",
							Image: "pximage",
							Args: []string{
								"-c", clusterName,
							},
						},
						{
							Name:  pxutil.TelemetryContainerName,
							Image: "telemetryImage",
						},
					},
				},
			},
		},
	}
	storkDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storkDeploymentName,
			Namespace: ds.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Image: "storkImage",
						},
					},
				},
			},
		},
	}
	autopilotDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      component.AutopilotDeploymentName,
			Namespace: ds.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Image: "autopilogImage",
						},
					},
				},
			},
		},
	}

	k8sClient := testutil.FakeK8sClient(
		ds, storkDeployment, autopilotDeployment,
	)

	if backupExits {
		err := k8sClient.Create(context.TODO(),
			&v1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      BackupConfigMapName,
					Namespace: ds.Namespace,
				},
			})
		require.NoError(t, err)
	}

	driver := testutil.MockDriver(gomock.NewController(t))
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetKubernetesClient(k8sClient)

	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().String().Return("mock-driver").AnyTimes()

	migrator := New(ctrl)
	go migrator.Start()

	err := wait.PollImmediate(time.Millisecond*200, time.Minute, func() (bool, error) {
		currentCluster := &corev1.StorageCluster{}
		err := testutil.Get(k8sClient, currentCluster, "px-cluster", ds.Namespace)
		if errors.IsNotFound(err) {
			return false, nil
		} else if err != nil {
			return false, err
		}

		// Approve the migration
		if currentCluster.Annotations[constants.AnnotationMigrationApproved] != "true" {
			currentCluster.Annotations[constants.AnnotationMigrationApproved] = "true"
			err = testutil.Update(k8sClient, currentCluster)
			require.NoError(t, err)
		}

		cm := v1.ConfigMap{}
		err = testutil.Get(k8sClient, &cm, BackupConfigMapName, ds.Namespace)
		if err == nil {
			// Precreated configmap is empty.
			if backupExits {
				require.Nil(t, cm.Data)
			} else {
				require.NotNil(t, cm.Data)
			}
			return true, nil
		} else if errors.IsNotFound(err) {
			return false, nil
		} else {
			return false, err
		}
	})
	require.NoError(t, err)
}
