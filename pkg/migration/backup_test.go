package migration

import (
	"fmt"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/libopenstorage/operator/drivers/storage/portworx/component"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
)

func TestBackup(t *testing.T) {
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

	driver := testutil.MockDriver(gomock.NewController(t))
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	ctrl.SetKubernetesClient(k8sClient)

	migrator := New(ctrl)

	driver.EXPECT().GetSelectorLabels().Return(nil).AnyTimes()
	driver.EXPECT().SetDefaultsOnStorageCluster(gomock.Any()).AnyTimes()
	driver.EXPECT().String().Return("mock-driver").AnyTimes()

	go migrator.Start()
	time.Sleep(10 * time.Second)

	// Verify the configmap is created
	cm := v1.ConfigMap{}
	err := testutil.Get(k8sClient, &cm, BackupConfigMapName, ds.Namespace)
	require.NoError(t, err)

	fmt.Printf("%v\n", cm.Data)
}
