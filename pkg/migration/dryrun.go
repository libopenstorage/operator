package migration

import (
	"fmt"

	"github.com/hashicorp/go-version"

	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"

	appsv1 "k8s.io/api/apps/v1"
)

func (h *Handler) dryRun(cluster *corev1.StorageCluster, ds *appsv1.DaemonSet) error {
	k8sVersion, err := k8sutil.GetVersion()
	if err != nil {
		return err
	}
	minVer, err := version.NewVersion("1.16")
	if err != nil {
		return fmt.Errorf("error parsing version '1.16': %v", err)
	}

	if !k8sVersion.GreaterThanOrEqual(minVer) {
		return fmt.Errorf("unsupported k8s version %v, please upgrade k8s to %s+ before migration", k8sVersion, minVer)
	}

	return nil
}
