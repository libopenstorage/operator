package migration

import (
	"context"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	"github.com/libopenstorage/operator/drivers/storage/portworx/mock"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	"github.com/libopenstorage/operator/pkg/constants"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"k8s.io/client-go/tools/record"
)

func TestBackup(t *testing.T) {
	testBackup(t, true, true)
	testBackup(t, false, false)
}

func testBackup(t *testing.T, backupExits, collectionExists bool) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "portworx",
			Namespace:       "portworx",
			UID:             types.UID("1001"),
			ResourceVersion: "100",
			SelfLink:        "portworx/portworx",
			Finalizers:      []string{"finalizer"},
			OwnerReferences: []metav1.OwnerReference{{Name: "owner"}},
			ClusterName:     "cluster-name",
			ManagedFields:   []metav1.ManagedFieldsEntry{{Manager: "manager"}},
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
	portworxService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxServiceName,
			Namespace: ds.Namespace,
		},
		Spec: v1.ServiceSpec{
			ClusterIP:  "1.2.3.4",
			ClusterIPs: []string{"1.2.3.4"},
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
							Image: "autopilotImage",
						},
					},
				},
			},
		},
	}
	csiService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      csiServiceName,
			Namespace: ds.Namespace,
		},
		Spec: v1.ServiceSpec{
			ClusterIP: v1.ClusterIPNone,
		},
	}

	k8sClient := testutil.FakeK8sClient(
		ds, portworxService, storkDeployment, autopilotDeployment, csiService,
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
	if collectionExists {
		err := k8sClient.Create(context.TODO(),
			&v1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      CollectionConfigMapName,
					Namespace: ds.Namespace,
				},
			})
		require.NoError(t, err)
	}

	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetEventRecorder(record.NewFakeRecorder(10))
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).Return(true).AnyTimes()

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
				require.NotContains(t, cm.Data["backup"], "uid")
				require.NotContains(t, cm.Data["backup"], "resourceVersion")
				require.NotContains(t, cm.Data["backup"], "generat")
				require.NotContains(t, cm.Data["backup"], "selfLink")
				require.NotContains(t, cm.Data["backup"], "finalizers")
				require.NotContains(t, cm.Data["backup"], "ownerReferences")
				require.NotContains(t, cm.Data["backup"], "clusterName")
				require.NotContains(t, cm.Data["backup"], "managedFields")
				require.NotContains(t, cm.Data["backup"], "clusterIP: 1.2.3.4")
				require.NotContains(t, cm.Data["backup"], "clusterIPs")
			}
		} else if errors.IsNotFound(err) {
			return false, nil
		} else {
			return false, err
		}

		cm = v1.ConfigMap{}
		err = testutil.Get(k8sClient, &cm, CollectionConfigMapName, ds.Namespace)
		if err == nil {
			// The configmap should be overwritten to non-empty if it precreated.
			require.NotNil(t, cm.Data)
			require.NotContains(t, cm.Data["backup"], "uid")
			require.NotContains(t, cm.Data["backup"], "resourceVersion")
			require.NotContains(t, cm.Data["backup"], "generat")
			require.NotContains(t, cm.Data["backup"], "selfLink")
			require.NotContains(t, cm.Data["backup"], "finalizers")
			require.NotContains(t, cm.Data["backup"], "ownerReferences")
			require.NotContains(t, cm.Data["backup"], "clusterName")
			require.NotContains(t, cm.Data["backup"], "managedFields")
			require.NotContains(t, cm.Data["backup"], "clusterIP: 1.2.3.4")
			require.NotContains(t, cm.Data["backup"], "clusterIPs")
			return true, nil
		} else if errors.IsNotFound(err) {
			return false, nil
		} else {
			return false, err
		}
	})
	require.NoError(t, err)
}
