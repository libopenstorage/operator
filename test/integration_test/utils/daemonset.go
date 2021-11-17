package utils

import (
	appsv1 "k8s.io/api/apps/v1"
)

// AddPortworxImageToDaemonSet adds the portworx image from the supplied
// version manifest to the given daemonset spec
func AddPortworxImageToDaemonSet(ds *appsv1.DaemonSet) error {
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
			break
		}
	}
	return nil
}
