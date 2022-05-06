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

// DeepEqualPod compares two pods.
func (h *DryRun) deepEqualPod(p1, p2 *v1.PodTemplateSpec) error {
	var msg string

	err := h.deepEqualVolumes(p1.Spec.Volumes, p2.Spec.Volumes)
	if err != nil {
		msg += fmt.Sprintf("Validate volumes failed: %s\n", err.Error())
	}

	err = h.deepEqualContainers(p1.Spec.Containers, p2.Spec.Containers)
	if err != nil {
		msg += fmt.Sprintf("Validate containers failed: %s\n", err.Error())
	}

	err = h.deepEqualContainers(p1.Spec.InitContainers, p2.Spec.InitContainers)
	if err != nil {
		msg += fmt.Sprintf("Validate init-containers failed: %s\n", err.Error())
	}

	if !reflect.DeepEqual(p1.Spec.ImagePullSecrets, p2.Spec.ImagePullSecrets) {
		msg += fmt.Sprintf("ImagePullSecrets are different: first %+v, second: %+v\n", p1.Spec.ImagePullSecrets, p2.Spec.ImagePullSecrets)
	}

	if msg != "" {
		return fmt.Errorf(msg)
	}
	return nil
}

func (h *DryRun) deepEqualVolumes(vol1, vol2 []v1.Volume) error {
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
		h.deepEqualVolume)
}

func (h *DryRun) deepEqualVolume(obj1, obj2 interface{}) error {
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

func (h *DryRun) deepEqualContainers(c1, c2 []v1.Container) error {
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
		h.deepEqualContainer)
}

// DeepEqualContainer compare two containers
func (h *DryRun) deepEqualContainer(obj1, obj2 interface{}) error {
	t1 := obj1.(v1.Container)
	t2 := obj2.(v1.Container)

	c1 := t1.DeepCopy()
	c2 := t2.DeepCopy()

	msg := ""
	if c1.Image != c2.Image {
		msg += fmt.Sprintf("image is different: %s, %s\n", c1.Image, c2.Image)
	}

	sort.Strings(c1.Command)
	sort.Strings(c2.Command)
	if !reflect.DeepEqual(c1.Command, c2.Command) {
		msg += fmt.Sprintf("command is different: %s, %s\n", t1.Command, t2.Command)
	}

	sort.Strings(c1.Args)
	sort.Strings(c2.Args)
	if !reflect.DeepEqual(c1.Args, c2.Args) {
		msg += fmt.Sprintf("args is different: %s, %s\n", t1.Args, t2.Args)
	}

	err := h.deepEqualEnvVars(c1.Env, c2.Env)
	if err != nil {
		msg += fmt.Sprintf("env vars is different, %v\n", err)
	}

	err = h.deepEqualVolumeMounts(c1.VolumeMounts, c2.VolumeMounts)
	if err != nil {
		msg += fmt.Sprintf("VolumeMounts is different, %v\n", err)
	}

	if msg != "" {
		return fmt.Errorf(msg)
	}
	return nil
}

func (h *DryRun) deepEqualEnvVars(env1, env2 []v1.EnvVar) error {
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

func (h *DryRun) deepEqualVolumeMounts(l1, l2 []v1.VolumeMount) error {
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
