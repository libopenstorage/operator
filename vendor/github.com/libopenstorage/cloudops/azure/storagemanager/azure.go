package storagemanager

import (
	"github.com/libopenstorage/cloudops"
	"github.com/libopenstorage/cloudops/common"
)

type azureStorageManager struct {
	decisionMatrix *cloudops.StorageDecisionMatrix
}

// NewAzureStorageManager returns an azure implementation for Storage Management
func NewAzureStorageManager(
	decisionMatrix cloudops.StorageDecisionMatrix,
) (cloudops.StorageManager, error) {
	return &azureStorageManager{&decisionMatrix}, nil
}

func (a *azureStorageManager) GetStorageDistribution(
	request *cloudops.StorageDistributionRequest,
) (*cloudops.StorageDistributionResponse, error) {
	return common.GetStorageDistribution(request, a.decisionMatrix)
}

func init() {
	cloudops.RegisterStorageManager(cloudops.Azure, NewAzureStorageManager)
}
