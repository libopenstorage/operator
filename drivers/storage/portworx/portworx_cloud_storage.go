package portworx

import (
	"github.com/libopenstorage/operator/pkg/apis/core/v1alpha2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type portworxCloudStorageManager struct {
	zoneCount     int
	cloudProvider string
	k8sClient     client.Client
}

func (p *portworxCloudStorageManager) GetNodeSpec(
	specs *v1alpha2.CloudStorageSpec,
) ([]v1alpha2.CloudStorageConfig, int, error) {
	// TODO
	return nil, 0, nil
}
