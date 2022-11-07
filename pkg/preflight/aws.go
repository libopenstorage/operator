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
	labelClusterUID = "PX_PREFLIGHT_CLUSTER_UID"
	labelVolumeName = "Name"

	// dryRunErrMsg dry run error response if succeeded, otherwise will be UnauthorizedOperation
	dryRunErrMsg = "DryRunOperation"
)

var (
	dryRunOption = map[string]string{cloudops.DryRunOption: ""}
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
	logrus.Info("preflight starting eks cloud permission check")

	// List volume on cluster UID first, Describe permission checked here first
	result, err := a.ops.Enumerate(nil, map[string]string{labelClusterUID: string(cluster.UID)}, "")
	if err != nil {
		logrus.Errorf("preflight failed to enumerate volumes: %v", err)
		return err
	}
	volumes := result[cloudops.SetIdentifierNone]

	// Dry run requires an existing volume, so create one or get an old one, delete in the end
	var vol *ec2.Volume
	if len(volumes) > 0 {
		// Reuse old volume for permission checks
		logrus.Infof("preflight found %v volumes, using the first one for permission check", len(volumes))
		vol = volumes[0].(*ec2.Volume)
	} else {
		// Create a new volume
		volName := volumePrefix + cluster.Name + "-" + uuid.New()
		labels := map[string]string{
			labelVolumeName: volName,
			labelClusterUID: string(cluster.UID),
		}
		v, err := a.ops.Create(a.getEC2VolumeTemplate(), labels, nil)
		if err != nil {
			logrus.WithError(err).Errorf("preflight failed to create eks volume %s", volName)
			return err
		}
		volumes = append(volumes, v)
		vol = v.(*ec2.Volume)
	}

	// Dry run the rest operations
	// Attach volume
	// without dry run since container doesn't have access to /dev/ directory on host, it will fail to return dev path
	if _, err := a.ops.Attach(*vol.VolumeId, dryRunOption); !dryRunSucceeded(err) {
		return fmt.Errorf("preflight volume attach dry run failed: %v", err)
	}

	// Expand volume
	if _, err := a.ops.Expand(*vol.VolumeId, uint64(2), dryRunOption); !dryRunSucceeded(err) {
		return fmt.Errorf("preflight volume expansion dry run failed: %v", err)
	}

	// Apply and remove tags
	tags := map[string]string{
		"foo": "bar",
	}
	if err := a.ops.ApplyTags(*vol.VolumeId, tags, dryRunOption); !dryRunSucceeded(err) {
		return fmt.Errorf("preflight volume tag dry run failed: %v", err)
	}
	if err := a.ops.RemoveTags(*vol.VolumeId, tags, dryRunOption); !dryRunSucceeded(err) {
		return fmt.Errorf("preflight volume remove tag dry run failed: %v", err)
	}

	// Detach volume
	if err := a.ops.Detach(*vol.VolumeId, dryRunOption); !dryRunSucceeded(err) {
		return fmt.Errorf("preflight volume detach dry run failed: %v", err)
	}

	// Delete volume
	// Check permission first then do the actual deletion in cleanup phase
	if err := a.ops.Delete(*vol.VolumeId, dryRunOption); !dryRunSucceeded(err) {
		return fmt.Errorf("preflight volume delete dry run failed: %v", err)
	}

	// Do a cleanup when preflight passed, as it's guaranteed to have permissions for volume deletion
	// Will delete volumes created in previous attempts as well by cluster ID label
	a.cleanupPreflightVolumes(volumes)
	logrus.Infof("preflight check for eks cloud permission passed")
	return nil
}

func (a *aws) cleanupPreflightVolumes(volumes []interface{}) {
	logrus.Infof("preflight cleaning up volumes created in permission check, %v volumes to delete", len(volumes))
	for _, v := range volumes {
		vol := *v.(*ec2.Volume).VolumeId
		err := a.ops.Delete(vol, nil)
		if err != nil {
			logrus.Warnf("preflight failed to delete volume %s: %v", vol, err)
		}
	}
}

func dryRunSucceeded(err error) bool {
	return err != nil && strings.Contains(err.Error(), dryRunErrMsg)
}
