package portworx

import (
	"context"
	"os"
	"path"
	"reflect"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/cloudops"
	"github.com/libopenstorage/cloudops/mock"
	"github.com/libopenstorage/cloudops/pkg/parser"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/cloudstorage"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	testProviderType = cloudops.ProviderType("mock")
	testNamespace    = "test-ns"
)

var (
	mockStorageManager *mock.MockStorageManager
)

func TestGetStorageNodeConfigNoConfigMap(t *testing.T) {
	k8sClient := fakeK8sClient()
	p := &portworxCloudStorage{
		cloudProvider: testProviderType,
		namespace:     testNamespace,
		k8sClient:     k8sClient,
	}
	_, err := p.GetStorageNodeConfig(nil, 1)
	require.Error(t, err, "Expected an error when no config map exits")
}

func TestGetStorageNodeConfigEmptyConfigMap(t *testing.T) {
	k8sClient := fakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: testNamespace,
			},
		},
	)
	p := &portworxCloudStorage{
		cloudProvider: testProviderType,
		namespace:     testNamespace,
		k8sClient:     k8sClient,
	}
	_, err := p.GetStorageNodeConfig(nil, 1)
	require.Error(t, err, "Expected an error when incorrect config map exists")
	require.Contains(t, err.Error(), "could not find decision matrix", "Unexpected error")
}

func TestGetStorageNodeConfigInvalidConfigMapNoKey(t *testing.T) {
	k8sClient := fakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: testNamespace,
			},
			Data: map[string]string{
				"foo": "bar",
			},
		},
	)
	p := &portworxCloudStorage{
		cloudProvider: testProviderType,
		namespace:     testNamespace,
		k8sClient:     k8sClient,
	}
	_, err := p.GetStorageNodeConfig(nil, 1)
	require.Error(t, err, "Expected an error when there is an incorrect config map")
	require.Contains(t, err.Error(), "could not find decision matrix", "Unexpected error")
}

func TestGetStorageNodeConfigInvalidConfigMapData(t *testing.T) {
	k8sClient := fakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: testNamespace,
			},
			Data: map[string]string{
				storageDecisionMatrixCMKey: "bar",
			},
		},
	)
	p := &portworxCloudStorage{
		cloudProvider: testProviderType,
		namespace:     testNamespace,
		k8sClient:     k8sClient,
	}
	_, err := p.GetStorageNodeConfig(nil, 1)
	require.Error(t, err, "Expected an error when invalid data is present in config map")
}

func TestGetStorageNodeConfigValidConfigMap(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	setupMockStorageManager(mockCtrl)

	_, yamlData := generateValidYamlData(t)

	k8sClient := fakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: testNamespace,
			},
			Data: map[string]string{
				storageDecisionMatrixCMKey: string(yamlData),
			},
		},
	)
	p := &portworxCloudStorage{
		cloudProvider:      testProviderType,
		namespace:          testNamespace,
		zoneToInstancesMap: map[string]int{"a": 3, "b": 3, "c": 3},
		k8sClient:          k8sClient,
	}

	inputSpecs := []corev1alpha1.CloudStorageCapacitySpec{
		{
			MinIOPS:          uint32(100),
			MinCapacityInGiB: uint64(100),
			MaxCapacityInGiB: uint64(200),
			Options:          map[string]string{"foo1": "bar1", "foo2": "bar2"},
		},
		{
			MinIOPS:          uint32(200),
			MinCapacityInGiB: uint64(200),
			MaxCapacityInGiB: uint64(500),
			Options:          map[string]string{"foo3": "bar3", "foo4": "bar4"},
		},
	}
	inputInstancesPerZone := 1

	mockStorageManager.EXPECT().
		GetStorageDistribution(&cloudops.StorageDistributionRequest{
			ZoneCount:        len(p.zoneToInstancesMap),
			InstancesPerZone: inputInstancesPerZone,
			UserStorageSpec: []*cloudops.StorageSpec{
				{
					IOPS:        uint32(100),
					MinCapacity: uint64(100),
					MaxCapacity: uint64(200),
				},
				{
					IOPS:        uint32(200),
					MinCapacity: uint64(200),
					MaxCapacity: uint64(500),
				},
			},
		}).
		Return(&cloudops.StorageDistributionResponse{
			InstanceStorage: []*cloudops.StoragePoolSpec{
				{
					DriveCapacityGiB: uint64(120),
					DriveType:        "foo",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint32(110),
				},
				{
					DriveCapacityGiB: uint64(220),
					DriveType:        "bar",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint32(210),
				},
			},
		}, nil)

	expectedResponse := &cloudstorage.Config{
		StorageInstancesPerZone: inputInstancesPerZone,
		CloudStorage: []cloudstorage.CloudDriveConfig{
			{
				Type:      "foo",
				SizeInGiB: uint64(120),
				IOPS:      uint32(110),
				Options:   map[string]string{"foo1": "bar1", "foo2": "bar2"},
			},
			{
				Type:      "bar",
				SizeInGiB: uint64(220),
				IOPS:      uint32(210),
				Options:   map[string]string{"foo3": "bar3", "foo4": "bar4"},
			},
		},
	}
	actualResponse, err := p.GetStorageNodeConfig(inputSpecs, inputInstancesPerZone)
	require.NoError(t, err, "Unexpected error on GetStorageNodeConfig")
	require.True(t, reflect.DeepEqual(*expectedResponse, *actualResponse), "Unexpected response: %v %v", actualResponse, expectedResponse)
}

func TestGetStorageNodeConfigDifferentInstancesPerZone(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	setupMockStorageManager(mockCtrl)
	_, yamlData := generateValidYamlData(t)

	k8sClient := fakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: testNamespace,
			},
			Data: map[string]string{
				storageDecisionMatrixCMKey: string(yamlData),
			},
		},
	)
	p := &portworxCloudStorage{
		cloudProvider:      testProviderType,
		namespace:          testNamespace,
		zoneToInstancesMap: map[string]int{"a": 3, "b": 3, "c": 3},
		k8sClient:          k8sClient,
	}

	inputSpecs := []corev1alpha1.CloudStorageCapacitySpec{
		{
			MinIOPS:          uint32(100),
			MinCapacityInGiB: uint64(100),
			MaxCapacityInGiB: uint64(200),
			Options:          map[string]string{"foo1": "bar1", "foo2": "bar2"},
		},
		{
			MinIOPS:          uint32(200),
			MinCapacityInGiB: uint64(200),
			MaxCapacityInGiB: uint64(500),
			Options:          map[string]string{"foo3": "bar3", "foo4": "bar4"},
		},
	}
	inputInstancesPerZone := 3
	outputInstancesPerZoneMin := 1
	outputInstancesPerZoneMax := 2

	mockStorageManager.EXPECT().
		GetStorageDistribution(&cloudops.StorageDistributionRequest{
			ZoneCount:        len(p.zoneToInstancesMap),
			InstancesPerZone: inputInstancesPerZone,
			UserStorageSpec: []*cloudops.StorageSpec{
				{
					IOPS:        uint32(100),
					MinCapacity: uint64(100),
					MaxCapacity: uint64(200),
				},
				{
					IOPS:        uint32(200),
					MinCapacity: uint64(200),
					MaxCapacity: uint64(500),
				},
			},
		}).
		Return(&cloudops.StorageDistributionResponse{
			InstanceStorage: []*cloudops.StoragePoolSpec{
				{
					DriveCapacityGiB: uint64(120),
					DriveType:        "foo",
					DriveCount:       1,
					InstancesPerZone: outputInstancesPerZoneMax,
					IOPS:             uint32(110),
				},
				{
					DriveCapacityGiB: uint64(220),
					DriveType:        "bar",
					DriveCount:       1,
					InstancesPerZone: outputInstancesPerZoneMin,
					IOPS:             uint32(210),
				},
			},
		}, nil)

	expectedResponse := &cloudstorage.Config{
		StorageInstancesPerZone: outputInstancesPerZoneMax,
		CloudStorage: []cloudstorage.CloudDriveConfig{
			{
				Type:      "foo",
				SizeInGiB: uint64(120),
				IOPS:      uint32(110),
				Options:   map[string]string{"foo1": "bar1", "foo2": "bar2"},
			},
			{
				Type:      "bar",
				SizeInGiB: uint64(220),
				IOPS:      uint32(210),
				Options:   map[string]string{"foo3": "bar3", "foo4": "bar4"},
			},
		},
	}
	actualResponse, err := p.GetStorageNodeConfig(inputSpecs, inputInstancesPerZone)
	require.NoError(t, err, "Unexpected error on GetStorageNodeConfig")
	require.True(t, reflect.DeepEqual(*expectedResponse, *actualResponse), "Unexpected response: %v %v", actualResponse, expectedResponse)
}

func TestGetStorageNodeConfigMultipleDriveCounts(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	setupMockStorageManager(mockCtrl)

	_, yamlData := generateValidYamlData(t)

	k8sClient := fakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: testNamespace,
			},
			Data: map[string]string{
				storageDecisionMatrixCMKey: string(yamlData),
			},
		},
	)
	p := &portworxCloudStorage{
		cloudProvider:      testProviderType,
		namespace:          testNamespace,
		zoneToInstancesMap: map[string]int{"a": 3, "b": 3, "c": 3},
		k8sClient:          k8sClient,
	}

	inputSpecs := []corev1alpha1.CloudStorageCapacitySpec{
		{
			MinIOPS:          uint32(100),
			MinCapacityInGiB: uint64(100),
			MaxCapacityInGiB: uint64(200),
			Options:          map[string]string{"foo1": "bar1", "foo2": "bar2"},
		},
		{
			MinIOPS:          uint32(200),
			MinCapacityInGiB: uint64(200),
			MaxCapacityInGiB: uint64(500),
			Options:          map[string]string{"foo3": "bar3", "foo4": "bar4"},
		},
	}
	inputInstancesPerZone := 1

	mockStorageManager.EXPECT().
		GetStorageDistribution(&cloudops.StorageDistributionRequest{
			ZoneCount:        len(p.zoneToInstancesMap),
			InstancesPerZone: inputInstancesPerZone,
			UserStorageSpec: []*cloudops.StorageSpec{
				{
					IOPS:        uint32(100),
					MinCapacity: uint64(100),
					MaxCapacity: uint64(200),
				},
				{
					IOPS:        uint32(200),
					MinCapacity: uint64(200),
					MaxCapacity: uint64(500),
				},
			},
		}).
		Return(&cloudops.StorageDistributionResponse{
			InstanceStorage: []*cloudops.StoragePoolSpec{
				{
					DriveCapacityGiB: uint64(120),
					DriveType:        "foo",
					DriveCount:       2,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint32(110),
				},
				{
					DriveCapacityGiB: uint64(220),
					DriveType:        "bar",
					DriveCount:       3,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint32(210),
				},
			},
		}, nil)

	expectedResponse := &cloudstorage.Config{
		StorageInstancesPerZone: inputInstancesPerZone,
		CloudStorage: []cloudstorage.CloudDriveConfig{
			{
				Type:      "foo",
				SizeInGiB: uint64(120),
				IOPS:      uint32(110),
				Options:   map[string]string{"foo1": "bar1", "foo2": "bar2"},
			},
			{
				Type:      "foo",
				SizeInGiB: uint64(120),
				IOPS:      uint32(110),
				Options:   map[string]string{"foo1": "bar1", "foo2": "bar2"},
			},
			{
				Type:      "bar",
				SizeInGiB: uint64(220),
				IOPS:      uint32(210),
				Options:   map[string]string{"foo3": "bar3", "foo4": "bar4"},
			},
			{
				Type:      "bar",
				SizeInGiB: uint64(220),
				IOPS:      uint32(210),
				Options:   map[string]string{"foo3": "bar3", "foo4": "bar4"},
			},
			{
				Type:      "bar",
				SizeInGiB: uint64(220),
				IOPS:      uint32(210),
				Options:   map[string]string{"foo3": "bar3", "foo4": "bar4"},
			},
		},
	}
	actualResponse, err := p.GetStorageNodeConfig(inputSpecs, inputInstancesPerZone)
	require.NoError(t, err, "Unexpected error on GetStorageNodeConfig")
	require.True(t, reflect.DeepEqual(*expectedResponse, *actualResponse), "Unexpected response: %v %v", actualResponse, expectedResponse)
}

func TestGetStorageNodeConfigSpecCountMismatch(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	setupMockStorageManager(mockCtrl)

	_, yamlData := generateValidYamlData(t)

	k8sClient := fakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: testNamespace,
			},
			Data: map[string]string{
				storageDecisionMatrixCMKey: string(yamlData),
			},
		},
	)
	p := &portworxCloudStorage{
		cloudProvider:      testProviderType,
		namespace:          testNamespace,
		zoneToInstancesMap: map[string]int{"a": 3, "b": 3, "c": 3},
		k8sClient:          k8sClient,
	}

	inputSpecs := []corev1alpha1.CloudStorageCapacitySpec{
		{
			MinIOPS:          uint32(100),
			MinCapacityInGiB: uint64(100),
			MaxCapacityInGiB: uint64(200),
			Options:          map[string]string{"foo1": "bar1", "foo2": "bar2"},
		},
		{
			MinIOPS:          uint32(200),
			MinCapacityInGiB: uint64(200),
			MaxCapacityInGiB: uint64(500),
			Options:          map[string]string{"foo3": "bar3", "foo4": "bar4"},
		},
	}
	inputInstancesPerZone := 1

	mockStorageManager.EXPECT().
		GetStorageDistribution(&cloudops.StorageDistributionRequest{
			ZoneCount:        len(p.zoneToInstancesMap),
			InstancesPerZone: inputInstancesPerZone,
			UserStorageSpec: []*cloudops.StorageSpec{
				{
					IOPS:        uint32(100),
					MinCapacity: uint64(100),
					MaxCapacity: uint64(200),
				},
				{
					IOPS:        uint32(200),
					MinCapacity: uint64(200),
					MaxCapacity: uint64(500),
				},
			},
		}).
		Return(&cloudops.StorageDistributionResponse{
			InstanceStorage: []*cloudops.StoragePoolSpec{
				{
					DriveCapacityGiB: uint64(120),
					DriveType:        "foo",
					DriveCount:       1,
					InstancesPerZone: inputInstancesPerZone,
					IOPS:             uint32(110),
				},
			},
		}, nil)

	_, err := p.GetStorageNodeConfig(inputSpecs, inputInstancesPerZone)
	require.Error(t, err, "Expected an error on GetStorageNodeConfig")
	require.Contains(t, err.Error(), "got an incorrect storage distribution", "Expected a different error")
}

func TestCreateStorageDistributionMatrixNotSupportedProviders(t *testing.T) {
	matrixSetup(t)
	defer matrixCleanup(t)

	k8sClient := fakeK8sClient()
	p := &portworxCloudStorage{
		cloudProvider: cloudops.AWS,
		namespace:     testNamespace,
		k8sClient:     k8sClient,
	}
	err := p.CreateStorageDistributionMatrix()
	require.Error(t, err, "Expected an error on cloud provider: %v", p.cloudProvider)

	p.cloudProvider = cloudops.GCE

	err = p.CreateStorageDistributionMatrix()
	require.Error(t, err, "Expected an error on cloud provider: %v", p.cloudProvider)

	p.cloudProvider = cloudops.Vsphere

	err = p.CreateStorageDistributionMatrix()
	require.Error(t, err, "Expected an error on cloud provider: %v", p.cloudProvider)
}

func TestCreateStorageDistributionMatrixSupportedProvider(t *testing.T) {
	matrixSetup(t)
	defer matrixCleanup(t)

	k8sClient := fakeK8sClient()
	p := &portworxCloudStorage{
		cloudProvider: cloudops.Azure,
		namespace:     testNamespace,
		k8sClient:     k8sClient,
		ownerRef:      &metav1.OwnerReference{},
	}
	err := p.CreateStorageDistributionMatrix()
	require.NoError(t, err, "Unexpected error on CreateStorageDistributionMatrix")

	cm := &v1.ConfigMap{}
	err = p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      storageDecisionMatrixCMName,
			Namespace: p.namespace,
		},
		cm,
	)
	require.NoError(t, err, "Expected config map to be created")
}

func TestCreateStorageDistributionMatrixAlreadyExists(t *testing.T) {
	// Not setting up the specs directory
	// Faking a config map
	k8sClient := fakeK8sClient(
		&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageDecisionMatrixCMName,
				Namespace: testNamespace,
			},
		},
	)
	// We should not get any errors
	p := &portworxCloudStorage{
		cloudProvider: cloudops.Azure,
		namespace:     testNamespace,
		k8sClient:     k8sClient,
	}
	err := p.CreateStorageDistributionMatrix()
	require.NoError(t, err, "Unexpected error on CreateStorageDistributionMatrix")
}

func matrixSetup(t *testing.T) {
	linkPath := path.Join(os.Getenv("GOPATH"), "src/github.com/libopenstorage/operator/vendor/github.com/libopenstorage/cloudops/specs")
	err := os.Symlink(linkPath, "specs")
	require.NoError(t, err, "failed to create symlink")
}

func matrixCleanup(t *testing.T) {
	err := os.RemoveAll("specs")
	require.NoError(t, err, "failed to remove specs directory")
}

func generateValidYamlData(t *testing.T) (cloudops.StorageDecisionMatrix, []byte) {
	inputMatrix := cloudops.StorageDecisionMatrix{
		Rows: []cloudops.StorageDecisionMatrixRow{
			{
				IOPS:         uint32(1000),
				MinSize:      uint64(100),
				MaxSize:      uint64(200),
				InstanceType: "foo",
			},
			{
				IOPS:         uint32(2000),
				MinSize:      uint64(200),
				MaxSize:      uint64(400),
				InstanceType: "bar",
			},
		},
	}
	p := parser.NewStorageDecisionMatrixParser()
	yamlBytes, err := p.MarshalToBytes(&inputMatrix)
	require.NoError(t, err, "Unexpected error on MarshalToYaml")
	return inputMatrix, yamlBytes
}

func setupMockStorageManager(mockCtrl *gomock.Controller) {
	mockStorageManager = mock.NewMockStorageManager(mockCtrl)

	initFn := func(matrix cloudops.StorageDecisionMatrix) (cloudops.StorageManager, error) {
		return mockStorageManager, nil
	}

	_, err := cloudops.NewStorageManager(cloudops.StorageDecisionMatrix{}, cloudops.ProviderType("mock"))
	if err != nil {
		// mock is not registered
		cloudops.RegisterStorageManager(
			testProviderType,
			initFn,
		)
	}
}
