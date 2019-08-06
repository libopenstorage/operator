package portworx

import (
	"github.com/libopenstorage/operator/pkg/apis/core/v1alpha2"
	"github.com/libopenstorage/operator/pkg/cloudstorage"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	decisionMatrixCMName = "portworx-cloud-storage-distribution"
)

type portworxCloudStorageManager struct {
	zoneCount     int
	cloudProvider string
	k8sClient     client.Client
}

func (p *portworxCloudStorageManager) GetStorageNodeConfig(
	specs *v1alpha2.CloudStorageSpec,
) (*cloudstorage.Config, error) {
	// TODO
	return nil, nil
}
