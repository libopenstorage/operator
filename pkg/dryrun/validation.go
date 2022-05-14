package dryrun

import (
	"fmt"
	"reflect"
	"sort"

	v1 "k8s.io/api/core/v1"

	"github.com/libopenstorage/operator/pkg/util"
)

var (
	// "crioconf" only exists with operator install.
	// "dev" only exists with daemonset install.
	// varlibosd only exists with operator install
	skipVolumes = map[string]bool{
		"crioconf":  true,
		"dev":       true,
		"varlibosd": true,
	}
)

func (d *DryRun) deepEqualService(p1, p2 *v1.Service) error {
	var msg string
	if p1.Spec.Type != p2.Spec.Type {
		msg += fmt.Sprintf("service type is different, before-migration %s, after-migration %s. ", p1.Spec.Type, p2.Spec.Type)
	}

	err := d.deepEqualServicePorts(p1.Spec.Ports, p2.Spec.Ports)
	if err != nil {
		msg += err.Error()
	}

	if msg != "" {
		return fmt.Errorf(msg)
	}
	return nil
}

func (d *DryRun) deepEqualServicePorts(l1, l2 []v1.ServicePort) error {
	getKey := func(v interface{}) string {
		s := v.(v1.ServicePort)
		return s.Name
	}

	getList := func(arr []v1.ServicePort) []interface{} {
		var objs []interface{}
		for _, obj := range arr {
			objs = append(objs, obj)
		}
		return objs
	}

	return util.DeepEqualObjects(
		getList(l1),
		getList(l2),
		getKey,
		util.DeepEqualObject)
}

func (d *DryRun) deepEqualCSIPod(p1, p2 *v1.PodTemplateSpec) error {
	filterFunc := func(p *v1.PodTemplateSpec) *v1.PodTemplateSpec {
		for i, c := range p.Spec.Containers {
			// To filter expected failure:
			// Containers are different: container csi-snapshot-controller is different: env vars is different, object \"ADDRESS\" exists in first array but does not exist in second array.\n\nVolumeMounts is different, object \"/csi\" exists in first array but does not exist in second array.\n\n\n\n
			if c.Name == "csi-snapshot-controller" {
				p.Spec.Containers[i].Env = nil
				p.Spec.Containers[i].VolumeMounts = nil
			}
		}
		return p
	}

	return d.deepEqualPod(filterFunc(p1), filterFunc(p2))
}

// deepEqualPod compares two pods.
func (d *DryRun) deepEqualPod(p1, p2 *v1.PodTemplateSpec) error {
	var msg string

	err := d.deepEqualVolumes(p1.Spec.Volumes, p2.Spec.Volumes)
	if err != nil {
		msg += fmt.Sprintf("Volumes are different: %s\n", err.Error())
	}

	err = d.deepEqualContainers(p1.Spec.Containers, p2.Spec.Containers)
	if err != nil {
		msg += fmt.Sprintf("Containers are different: %s\n", err.Error())
	}

	err = d.deepEqualContainers(p1.Spec.InitContainers, p2.Spec.InitContainers)
	if err != nil {
		msg += fmt.Sprintf("init-containers are different: %s\n", err.Error())
	}

	if !reflect.DeepEqual(p1.Spec.ImagePullSecrets, p2.Spec.ImagePullSecrets) {
		msg += fmt.Sprintf("ImagePullSecrets are different: first %+v, second: %+v\n", p1.Spec.ImagePullSecrets, p2.Spec.ImagePullSecrets)
	}

	if msg != "" {
		return fmt.Errorf(msg)
	}
	return nil
}

func (d *DryRun) deepEqualVolumes(vol1, vol2 []v1.Volume) error {
	// Use host path as key, as volume name may be different but path is the same.
	getVolumeKey := func(v interface{}) string {
		vol := v.(v1.Volume)

		if vol.HostPath != nil {
			return vol.HostPath.Path
		}

		return vol.Name
	}

	getVolumeList := func(arr []v1.Volume) []interface{} {
		var objs []interface{}
		for _, obj := range arr {
			if skipVolumes[obj.Name] {
				continue
			}
			objs = append(objs, obj)
		}
		return objs
	}

	return util.DeepEqualObjects(
		getVolumeList(vol1),
		getVolumeList(vol2),
		getVolumeKey,
		d.deepEqualVolume)
}

func (d *DryRun) deepEqualVolume(obj1, obj2 interface{}) error {
	t1 := obj1.(v1.Volume)
	t2 := obj2.(v1.Volume)

	vol1 := t1.DeepCopy()
	vol2 := t2.DeepCopy()

	cleanupFields := func(vol v1.Volume) v1.Volume {
		// We dont compare name, but only the content
		// Ignore hostPath type as nil may represent default value.
		vol.Name = ""
		if vol.HostPath != nil {
			vol.HostPath.Type = nil
		}
		return vol
	}

	return util.DeepEqualObject(cleanupFields(*vol1), cleanupFields(*vol2))
}

func (d *DryRun) deepEqualContainers(c1, c2 []v1.Container) error {
	getContainerName := func(v interface{}) string {
		return v.(v1.Container).Name
	}
	getContainerList := func(arr []v1.Container) []interface{} {
		objs := make([]interface{}, len(arr))
		for i, obj := range arr {
			objs[i] = obj
		}
		return objs
	}

	return util.DeepEqualObjects(
		getContainerList(c1),
		getContainerList(c2),
		getContainerName,
		d.deepEqualContainer)
}

// DeepEqualContainer compare two containers
func (d *DryRun) deepEqualContainer(obj1, obj2 interface{}) error {
	t1 := obj1.(v1.Container)
	t2 := obj2.(v1.Container)

	c1 := t1.DeepCopy()
	c2 := t2.DeepCopy()

	msg := ""
	if c1.Image != c2.Image {
		msg += fmt.Sprintf("image is different: before-migration %s, after-migration %s, please make sure the image exists if running on air-gapped env.\n", c1.Image, c2.Image)
	}

	sort.Strings(c1.Command)
	sort.Strings(c2.Command)
	if !reflect.DeepEqual(c1.Command, c2.Command) {
		msg += fmt.Sprintf("command is different: before-migration %s, after-migration %s\n", t1.Command, t2.Command)
	}

	sort.Strings(c1.Args)
	sort.Strings(c2.Args)
	if !reflect.DeepEqual(c1.Args, c2.Args) {
		msg += fmt.Sprintf("args is different: before-migration %s, after-migration %s\n", t1.Args, t2.Args)
	}

	err := d.deepEqualEnvVars(c1.Env, c2.Env)
	if err != nil {
		msg += fmt.Sprintf("env vars is different, %v\n", err)
	}

	err = d.deepEqualVolumeMounts(c1.VolumeMounts, c2.VolumeMounts)
	if err != nil {
		msg += fmt.Sprintf("VolumeMounts is different, %v\n", err)
	}

	if msg != "" {
		return fmt.Errorf("container %s is different: %s", c1.Name, msg)
	}
	return nil
}

func (d *DryRun) deepEqualEnvVars(env1, env2 []v1.EnvVar) error {
	getName := func(v interface{}) string {
		return v.(v1.EnvVar).Name
	}
	getList := func(arr []v1.EnvVar) []interface{} {
		var objs []interface{}
		skippedEnvs := map[string]bool{
			"PX_TEMPLATE_VERSION":  true,
			"PORTWORX_CSIVERSION":  true,
			"CSI_ENDPOINT":         true,
			"NODE_NAME":            true,
			"PX_NAMESPACE":         true,
			"PX_SECRETS_NAMESPACE": true,
		}
		for _, obj := range arr {
			if skippedEnvs[obj.Name] {
				continue
			}
			// Ignore API version compare, found "v1" is same to "" during test.
			if obj.ValueFrom != nil && obj.ValueFrom.FieldRef != nil {
				obj.ValueFrom.FieldRef.APIVersion = ""
			}
			objs = append(objs, obj)
		}
		return objs
	}

	return util.DeepEqualObjects(
		getList(env1),
		getList(env2),
		getName,
		util.DeepEqualObject)
}

func (d *DryRun) deepEqualVolumeMounts(l1, l2 []v1.VolumeMount) error {
	getName := func(v interface{}) string {
		return v.(v1.VolumeMount).MountPath
	}
	getList := func(arr []v1.VolumeMount) []interface{} {
		var objs []interface{}
		for _, obj := range arr {
			if skipVolumes[obj.Name] {
				continue
			}
			obj.Name = ""
			objs = append(objs, obj)
		}
		return objs
	}

	return util.DeepEqualObjects(
		getList(l1),
		getList(l2),
		getName,
		util.DeepEqualObject)
}
