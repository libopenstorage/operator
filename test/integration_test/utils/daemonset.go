package utils

import (
	appsv1 "k8s.io/api/apps/v1"
)

// UpdatePortworxDaemonSetImages adds the portworx image from the supplied
// version manifest to the given daemonset spec
func UpdatePortworxDaemonSetImages(ds *appsv1.DaemonSet) error {
	for i, c := range ds.Spec.Template.Spec.Containers {
		if c.Name == "portworx" {
			pxContainer := c.DeepCopy()
			pxContainer.Image = PxSpecImages["version"]
			env, err := addDefaultEnvVars(pxContainer.Env, PxSpecGenURL)
			if err != nil {
				return err
			}
			pxContainer.Env = env
			ds.Spec.Template.Spec.Containers[i] = *pxContainer
		} else if c.Name == "csi-node-driver-registrar" {
			container := c.DeepCopy()
			container.Image = PxSpecImages["csiNodeDriverRegistrar"]
			ds.Spec.Template.Spec.Containers[i] = *container
		}
	}
	return nil
}
