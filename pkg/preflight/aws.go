package preflight

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/opsworks"
	"github.com/pborman/uuid"
	"github.com/sirupsen/logrus"

	"github.com/libopenstorage/cloudops"
	awsops "github.com/libopenstorage/cloudops/aws"
	corev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
)

const (
	eksDistribution = "eks"
	volumePrefix    = "px-preflight-"
	labelClusterID  = "PX_PREFLIGHT_CLUSTER_ID"
	labelVolumeName = "Name"
)

type aws struct {
	checker
	ops  cloudops.Ops
	zone string
}

func (a *aws) initCloudOps() error {
	if a.ops != nil {
		return nil
	}

	// Initialize aws client for cloud drive permission checks
	// TODO: use provided credentials to create a client
	client, err := awsops.NewClient()
	if err != nil {
		return err
	}
	a.ops = client

	instance, err := client.InspectInstance(client.InstanceID())
	if err != nil {
		return err
	}
	a.zone = instance.Zone
	return nil
}

func (a *aws) getEC2VolumeTemplate() *ec2.Volume {
	volTypeGp2 := opsworks.VolumeTypeGp2
	volSize := int64(1)
	return &ec2.Volume{
		Size:             &volSize,
		VolumeType:       &volTypeGp2,
		AvailabilityZone: &a.zone,
	}
}

func (a *aws) CheckCloudDrivePermission(cluster *corev1.StorageCluster) error {
	// Only init the aws client when needed
	if err := a.initCloudOps(); err != nil {
		return err
	}
	// check the permission here by doing dummy drive operations
	// TODO: use dry run options
	logrus.Info("preflight starting eks cloud permission check")

	// Create volume
	volName := volumePrefix + cluster.Name + "-" + uuid.New()
	labels := map[string]string{
		labelVolumeName: volName,
		labelClusterID:  cluster.Name,
	}
	v, err := a.ops.Create(a.getEC2VolumeTemplate(), labels)
	if err != nil {
		logrus.WithError(err).Errorf("preflight failed to create eks volume %s", volName)
		return err
	}

	// Attach volume
	vol := v.(*ec2.Volume)
	_, err = a.ops.Attach(*vol.VolumeId, nil)
	if err != nil {
		// Operator container doesn't have access to /dev/ directory on the host, so we expect an error here
		// unable to map volume vol-03227127b9b7b442e with block device mapping /dev/xvdp to an actual device path on the host
		errGetPath := fmt.Sprintf("unable to map volume %s with block device mapping", *vol.VolumeId)
		errAlreadyAttached := fmt.Sprintf("%s is already attached to an instance", *vol.VolumeId)
		if !strings.Contains(err.Error(), errGetPath) && !strings.Contains(err.Error(), errAlreadyAttached) {
			logrus.WithError(err).Errorf("preflight failed to attach volume")
			return err
		}

		// Try to get the volume attachment info
		volumes, err := a.ops.Inspect([]*string{vol.VolumeId})
		if err != nil || len(volumes) != 1 {
			logrus.WithError(err).Errorf("preflight failed to inspect volume")
			return err
		}

		// Inspect volume attachments
		vol = volumes[0].(*ec2.Volume)
		if len(vol.Attachments) != 1 || *vol.Attachments[0].State != ec2.VolumeAttachmentStateAttached {
			logrus.WithFields(logrus.Fields{
				"attachments": vol.Attachments,
			}).Errorf("preflight volume is not attached")
			return fmt.Errorf("preflight volume is not attached")
		}
	}

	// Detach
	if err := a.ops.Detach(*vol.VolumeId); err != nil {
		//  IncorrectState: Volume 'vol-0dd22e588c5dfee1e' is in the 'available' state.\n\tstatus code: 400, request id: e8a7f095-138f-4271-840b-c7ae08a89529
		errAlreadyDetached := fmt.Sprintf("Volume '%s' is in the 'available' state", *vol.VolumeId)
		if !strings.Contains(err.Error(), errAlreadyDetached) {
			logrus.WithError(err).Errorf("preflight failed to detach volume")
			return err
		}
	}

	if err := a.ops.Delete(*vol.VolumeId); err != nil {
		logrus.WithError(err).Errorf("preflight failed to delete volume")
		return err
	}

	// Do a cleanup when preflight passed, as it's guaranteed to have delete permission
	a.cleanupPreflightVolumes(cluster.Name)
	logrus.Infof("preflight check for eks cloud permission passed")
	return nil
}

func (a *aws) cleanupPreflightVolumes(clusterName string) {
	logrus.Infof("preflight cleaning up volumes created in permission check")
	result, err := a.ops.Enumerate(nil, map[string]string{labelClusterID: clusterName}, "")
	if err != nil {
		logrus.Warnf("preflight failed to list volumes to clean up")
		return
	}

	logrus.Infof("preflight found %v volumes to clean up", len(result[cloudops.SetIdentifierNone]))
	for _, v := range result[cloudops.SetIdentifierNone] {
		vol := *v.(*ec2.Volume).VolumeId
		err := a.ops.Detach(vol)
		if err == nil {
			logrus.Infof("preflight volume %s detached", vol)
		}
		err = a.ops.Delete(vol)
		if err == nil {
			logrus.Infof("preflight volume %s deleted", vol)
		}
	}
}
