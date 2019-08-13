package portworx

import (
	"context"
	"fmt"
	"math"
	"path"

	"github.com/libopenstorage/cloudops"
	"github.com/libopenstorage/cloudops/pkg/parser"

	// importing the decisionmatrix package from cloudops to vendor
	// the cloud specific decision matrices
	_ "github.com/libopenstorage/cloudops/specs/decisionmatrix"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/cloudstorage"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	storageDecisionMatrixCMName = "portworx-storage-decision-matrix"
	storageDecisionMatrixCMKey  = "matrix"
	specDir                     = "specs/decisionmatrix"
)

type portworxCloudStorage struct {
	zoneToInstancesMap map[string]int
	cloudProvider      cloudops.ProviderType
	namespace          string
	k8sClient          client.Client
}

func (p *portworxCloudStorage) GetStorageNodeConfig(
	specs []corev1alpha1.CloudStorageCapacitySpec,
	instancesPerZone int,
) (*cloudstorage.Config, error) {
	// Get the decision matrix config map
	cm := &v1.ConfigMap{}
	err := p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      storageDecisionMatrixCMName,
			Namespace: p.namespace,
		},
		cm,
	)
	if err != nil {
		return nil, fmt.Errorf("could not retrieve %v config map: %v", storageDecisionMatrixCMName, err)
	}
	matrix, ok := cm.Data[storageDecisionMatrixCMKey]
	if !ok {
		return nil, fmt.Errorf(
			"could not find decision matrix in %v config map at key %v",
			storageDecisionMatrixCMName, storageDecisionMatrixCMKey,
		)
	}
	matrixBytes := []byte(matrix)

	decisionMatrix, err := parser.NewStorageDecisionMatrixParser().UnmarshalFromBytes(matrixBytes)
	if err != nil {
		return nil, err
	}

	cloudopsStorageManager, err := cloudops.NewStorageManager(
		*decisionMatrix,
		cloudops.ProviderType(p.cloudProvider),
	)
	if err != nil {
		return nil, err
	}

	if instancesPerZone == 0 && len(p.zoneToInstancesMap) > 0 {
		// Find out the minimum no. of instances out of all zones
		minInstances := math.MaxInt32
		for _, instances := range p.zoneToInstancesMap {
			if minInstances > instances {
				minInstances = instances
			}
		}
		instancesPerZone = minInstances
	}

	distributionRequest := p.capacitySpecToStorageDistributionRequest(
		specs,
		instancesPerZone,
	)

	distributionResponse, err := cloudopsStorageManager.GetStorageDistribution(distributionRequest)
	if err != nil {
		return nil, err
	}
	if len(specs) != len(distributionResponse.InstanceStorage) {
		// We should get same amount of instance storages (pools) as the user had requested
		return nil, fmt.Errorf("got an incorrect storage distribution: number of "+
			"input storage specs (%v) do not match with the output specs (%v)",
			len(specs), len(distributionResponse.InstanceStorage),
		)
	}
	return p.storageDistributionResponseToCloudConfig(
		specs,
		distributionResponse,
	), nil
}

func (p *portworxCloudStorage) CreateStorageDistributionMatrix() error {
	cm := &v1.ConfigMap{}
	err := p.k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      storageDecisionMatrixCMName,
			Namespace: p.namespace,
		},
		cm,
	)
	if err == nil {
		// config map already exists
		return nil
	}
	yamlParser := parser.NewStorageDecisionMatrixParser()
	matrixFileName := path.Join(specDir, string(p.cloudProvider)+".yaml")
	matrix, err := yamlParser.UnmarshalFromYaml(matrixFileName)
	if err != nil {
		return err
	}
	yamlBytes, err := yamlParser.MarshalToBytes(matrix)
	if err != nil {
		return err
	}

	decisionMatrixCM := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      storageDecisionMatrixCMName,
			Namespace: p.namespace,
		},
		Data: map[string]string{
			storageDecisionMatrixCMKey: string(yamlBytes),
		},
	}
	return p.k8sClient.Create(
		context.TODO(),
		decisionMatrixCM,
	)
}

func (p *portworxCloudStorage) capacitySpecToStorageDistributionRequest(
	specs []corev1alpha1.CloudStorageCapacitySpec,
	instancesPerZone int,
) *cloudops.StorageDistributionRequest {
	request := &cloudops.StorageDistributionRequest{
		ZoneCount:        len(p.zoneToInstancesMap),
		InstancesPerZone: instancesPerZone,
	}
	for _, spec := range specs {
		request.UserStorageSpec = append(
			request.UserStorageSpec,
			&cloudops.StorageSpec{
				IOPS:        spec.MinIOPS,
				MinCapacity: spec.MinCapacityInGiB,
				MaxCapacity: spec.MaxCapacityInGiB,
			},
		)
	}
	return request
}

func (p *portworxCloudStorage) storageDistributionResponseToCloudConfig(
	specs []corev1alpha1.CloudStorageCapacitySpec,
	response *cloudops.StorageDistributionResponse,
) *cloudstorage.Config {
	config := &cloudstorage.Config{}
	maxInstancesPerZone := 0
	for j, instanceStorage := range response.InstanceStorage {
		for i := 0; i < int(instanceStorage.DriveCount); i++ {
			driveConfig := cloudstorage.CloudDriveConfig{
				Type:      instanceStorage.DriveType,
				SizeInGiB: instanceStorage.DriveCapacityGiB,
				IOPS:      instanceStorage.IOPS,
				Options:   specs[j].Options,
			}
			config.CloudStorage = append(config.CloudStorage, driveConfig)
			// TODO: Currently we choose the maximum value of instances per zone
			// from the list of InstanceStorage. Ideally each InstanceStorage which
			// maps to a StoragePool can have its own instances per zone value, but until
			// support is added in Portworx we will have to choose the max value.
			if instanceStorage.InstancesPerZone > maxInstancesPerZone {
				maxInstancesPerZone = instanceStorage.InstancesPerZone
			}
		}
	}
	config.StorageInstancesPerZone = maxInstancesPerZone
	return config
}
