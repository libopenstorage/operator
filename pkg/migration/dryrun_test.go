package migration

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	k8sversion "k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"

	"github.com/libopenstorage/operator/drivers/storage/portworx/manifest"
	"github.com/libopenstorage/operator/drivers/storage/portworx/mock"
	pxutil "github.com/libopenstorage/operator/drivers/storage/portworx/util"
	"github.com/libopenstorage/operator/pkg/controller/storagecluster"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
)

func TestDryRun(t *testing.T) {
	clusterName := "px-cluster"
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "portworx",
			Namespace: "portworx",
		},
		Spec: appsv1.DaemonSetSpec{
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Volumes: []v1.Volume{
						{
							Name: "test",
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
					},
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

	k8sClient := testutil.FakeK8sClient(ds)
	mockController := gomock.NewController(t)
	driver := testutil.MockDriver(mockController)
	driver.EXPECT().GetStoragePodSpec(gomock.Any(), gomock.Any()).Return(ds.Spec.Template.Spec, nil).AnyTimes()
	ctrl := &storagecluster.Controller{
		Driver: driver,
	}
	recorder := record.NewFakeRecorder(10)
	ctrl.SetEventRecorder(recorder)
	ctrl.SetKubernetesClient(k8sClient)
	mockManifest := mock.NewMockManifest(mockController)
	manifest.SetInstance(mockManifest)
	mockManifest.EXPECT().CanAccessRemoteManifest(gomock.Any()).Return(true).AnyTimes()

	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &k8sversion.Info{
		GitVersion: "v1.15.0",
	}

	migrator := New(ctrl)
	go migrator.Start()

	err := wait.PollImmediate(time.Millisecond*200, time.Second*5, func() (bool, error) {
		if strings.Contains(reflect.ValueOf(<-recorder.Events).String(), "unsupported k8s version") {
			return true, nil
		}

		return false, nil
	})
	require.NoError(t, err)
}
