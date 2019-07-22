package k8s

import (
	"context"
	"reflect"

	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CreateOrUpdateServiceAccount creates a service account if not present,
// else updates it if it has changed
func CreateOrUpdateServiceAccount(
	k8sClient client.Client,
	sa *v1.ServiceAccount,
	ownerRef *metav1.OwnerReference,
) error {
	existingSA := &v1.ServiceAccount{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      sa.Name,
			Namespace: sa.Namespace,
		},
		existingSA,
	)
	if errors.IsNotFound(err) {
		logrus.Debugf("Creating %s service account", sa.Name)
		return k8sClient.Create(context.TODO(), sa)
	} else if err != nil {
		return err
	}

	for _, o := range existingSA.OwnerReferences {
		if o.UID != ownerRef.UID {
			sa.OwnerReferences = append(sa.OwnerReferences, o)
		}
	}

	if len(sa.OwnerReferences) > len(existingSA.OwnerReferences) {
		logrus.Debugf("Updating %s service account", sa.Name)
		return k8sClient.Update(context.TODO(), sa)
	}
	return nil
}

// DeleteServiceAccount deletes a service account if present
func DeleteServiceAccount(
	k8sClient client.Client,
	name, namespace string,
) error {
	resource := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}

	serviceAccount := &v1.ServiceAccount{}
	err := k8sClient.Get(context.TODO(), resource, serviceAccount)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	logrus.Debugf("Deleting %s service account", name)
	return k8sClient.Delete(context.TODO(), serviceAccount)
}

// CreateOrUpdateRole creates a role if not present,
// else updates it if it has changed
func CreateOrUpdateRole(
	k8sClient client.Client,
	role *rbacv1.Role,
	ownerRef *metav1.OwnerReference,
) error {
	existingRole := &rbacv1.Role{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      role.Name,
			Namespace: role.Namespace,
		},
		existingRole,
	)
	if errors.IsNotFound(err) {
		logrus.Debugf("Creating %s role", role.Name)
		return k8sClient.Create(context.TODO(), role)
	} else if err != nil {
		return err
	}

	modified := !reflect.DeepEqual(role.Rules, existingRole.Rules)

	for _, o := range existingRole.OwnerReferences {
		if o.UID != ownerRef.UID {
			role.OwnerReferences = append(role.OwnerReferences, o)
		}
	}

	if modified || len(role.OwnerReferences) > len(existingRole.OwnerReferences) {
		logrus.Debugf("Updating %s role", role.Name)
		return k8sClient.Update(context.TODO(), role)
	}
	return nil
}

// DeleteRole deletes a role if present
func DeleteRole(
	k8sClient client.Client,
	name, namespace string,
) error {
	resource := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}

	role := &rbacv1.Role{}
	err := k8sClient.Get(context.TODO(), resource, role)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	logrus.Debugf("Deleting %s role", name)
	return k8sClient.Delete(context.TODO(), role)
}

// CreateOrUpdateRoleBinding creates a role binding if not present,
// else updates it if it has changed
func CreateOrUpdateRoleBinding(
	k8sClient client.Client,
	rb *rbacv1.RoleBinding,
	ownerRef *metav1.OwnerReference,
) error {
	existingRB := &rbacv1.RoleBinding{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      rb.Name,
			Namespace: rb.Namespace,
		},
		existingRB,
	)
	if errors.IsNotFound(err) {
		logrus.Debugf("Creating %s role binding", rb.Name)
		return k8sClient.Create(context.TODO(), rb)
	} else if err != nil {
		return err
	}

	modified := !reflect.DeepEqual(rb.Subjects, existingRB.Subjects) ||
		!reflect.DeepEqual(rb.RoleRef, existingRB.RoleRef)

	for _, o := range existingRB.OwnerReferences {
		if o.UID != ownerRef.UID {
			rb.OwnerReferences = append(rb.OwnerReferences, o)
		}
	}

	if modified || len(rb.OwnerReferences) > len(existingRB.OwnerReferences) {
		logrus.Debugf("Updating %s role binding", rb.Name)
		return k8sClient.Update(context.TODO(), rb)
	}
	return nil
}

// DeleteRoleBinding deletes a role binding if present
func DeleteRoleBinding(
	k8sClient client.Client,
	name, namespace string,
) error {
	resource := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}

	roleBinding := &rbacv1.RoleBinding{}
	err := k8sClient.Get(context.TODO(), resource, roleBinding)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	logrus.Debugf("Deleting %s role binding", name)
	return k8sClient.Delete(context.TODO(), roleBinding)
}

// CreateOrUpdateClusterRole creates a cluster role if not present,
// else updates it if it has changed
func CreateOrUpdateClusterRole(
	k8sClient client.Client,
	cr *rbacv1.ClusterRole,
	ownerRef *metav1.OwnerReference,
) error {
	existingCR := &rbacv1.ClusterRole{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{Name: cr.Name},
		existingCR,
	)
	if errors.IsNotFound(err) {
		logrus.Debugf("Creating %s cluster role", cr.Name)
		return k8sClient.Create(context.TODO(), cr)
	} else if err != nil {
		return err
	}

	modified := !reflect.DeepEqual(cr.Rules, existingCR.Rules)

	for _, o := range existingCR.OwnerReferences {
		if o.UID != ownerRef.UID {
			cr.OwnerReferences = append(cr.OwnerReferences, o)
		}
	}

	if modified || len(cr.OwnerReferences) > len(existingCR.OwnerReferences) {
		logrus.Debugf("Updating %s cluster role", cr.Name)
		return k8sClient.Update(context.TODO(), cr)
	}
	return nil
}

// DeleteClusterRole deletes a cluster role if present
func DeleteClusterRole(
	k8sClient client.Client,
	name string,
) error {
	resource := types.NamespacedName{
		Name: name,
	}

	clusterRole := &rbacv1.ClusterRole{}
	err := k8sClient.Get(context.TODO(), resource, clusterRole)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	logrus.Debugf("Deleting %s cluster role", name)
	return k8sClient.Delete(context.TODO(), clusterRole)
}

// CreateOrUpdateClusterRoleBinding creates a cluster role binding if not present,
// else updates it if it has changed
func CreateOrUpdateClusterRoleBinding(
	k8sClient client.Client,
	crb *rbacv1.ClusterRoleBinding,
	ownerRef *metav1.OwnerReference,
) error {
	existingCRB := &rbacv1.ClusterRoleBinding{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{Name: crb.Name},
		existingCRB,
	)
	if errors.IsNotFound(err) {
		logrus.Debugf("Creating %s cluster role binding", crb.Name)
		return k8sClient.Create(context.TODO(), crb)
	} else if err != nil {
		return err
	}

	modified := !reflect.DeepEqual(crb.Subjects, existingCRB.Subjects) ||
		!reflect.DeepEqual(crb.RoleRef, existingCRB.RoleRef)

	for _, o := range existingCRB.OwnerReferences {
		if o.UID != ownerRef.UID {
			crb.OwnerReferences = append(crb.OwnerReferences, o)
		}
	}

	if modified || len(crb.OwnerReferences) > len(existingCRB.OwnerReferences) {
		logrus.Debugf("Updating %s cluster role binding", crb.Name)
		return k8sClient.Update(context.TODO(), crb)
	}
	return nil
}

// DeleteClusterRoleBinding deletes a cluster role binding if present
func DeleteClusterRoleBinding(
	k8sClient client.Client,
	name string,
) error {
	resource := types.NamespacedName{
		Name: name,
	}

	clusterRoleBinding := &rbacv1.ClusterRoleBinding{}
	err := k8sClient.Get(context.TODO(), resource, clusterRoleBinding)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	logrus.Debugf("Deleting %s cluster role binding", name)
	return k8sClient.Delete(context.TODO(), clusterRoleBinding)
}

// CreateOrUpdateConfigMap creates a config map if not present,
// else updates it if it has changed
func CreateOrUpdateConfigMap(
	k8sClient client.Client,
	configMap *v1.ConfigMap,
	ownerRef *metav1.OwnerReference,
) error {
	existingConfigMap := &v1.ConfigMap{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      configMap.Name,
			Namespace: configMap.Namespace,
		},
		existingConfigMap,
	)
	if errors.IsNotFound(err) {
		logrus.Debugf("Creating %v config map", configMap.Name)
		return k8sClient.Create(context.TODO(), configMap)
	} else if err != nil {
		return err
	}

	modified := !reflect.DeepEqual(configMap.Data, existingConfigMap.Data) ||
		!reflect.DeepEqual(configMap.BinaryData, existingConfigMap.BinaryData)

	for _, o := range existingConfigMap.OwnerReferences {
		if o.UID != ownerRef.UID {
			configMap.OwnerReferences = append(configMap.OwnerReferences, o)
		}
	}

	if modified || len(configMap.OwnerReferences) > len(existingConfigMap.OwnerReferences) {
		logrus.Debugf("Updating %v config map", configMap.Name)
		return k8sClient.Update(context.TODO(), configMap)
	}
	return nil
}

// DeleteConfigMap deletes a config map if present
func DeleteConfigMap(
	k8sClient client.Client,
	name, namespace string,
) error {
	resource := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}

	configMap := &v1.ConfigMap{}
	err := k8sClient.Get(context.TODO(), resource, configMap)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	logrus.Debugf("Deleting %s config map", name)
	return k8sClient.Delete(context.TODO(), configMap)
}

// CreateOrUpdateStorageClass creates a storage class if not present,
// else updates it if it has changed
func CreateOrUpdateStorageClass(
	k8sClient client.Client,
	sc *storagev1.StorageClass,
	ownerRef *metav1.OwnerReference,
) error {
	existingSC := &storagev1.StorageClass{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{Name: sc.Name},
		existingSC,
	)
	if errors.IsNotFound(err) {
		logrus.Debugf("Creating %s storage class", sc.Name)
		return k8sClient.Create(context.TODO(), sc)
	} else if err != nil {
		return err
	}

	modified := !reflect.DeepEqual(sc.Provisioner, existingSC.Provisioner)

	for _, o := range existingSC.OwnerReferences {
		if o.UID != ownerRef.UID {
			sc.OwnerReferences = append(sc.OwnerReferences, o)
		}
	}

	if modified || len(sc.OwnerReferences) > len(existingSC.OwnerReferences) {
		logrus.Debugf("Updating %s storage class", sc.Name)
		return k8sClient.Update(context.TODO(), sc)
	}
	return nil
}

// DeleteStorageClass deletes a storage class if present
func DeleteStorageClass(
	k8sClient client.Client,
	name string,
) error {
	resource := types.NamespacedName{
		Name: name,
	}

	storageClass := &storagev1.StorageClass{}
	err := k8sClient.Get(context.TODO(), resource, storageClass)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	logrus.Debugf("Deleting %s storage class", name)
	return k8sClient.Delete(context.TODO(), storageClass)
}

// CreateOrUpdateService creates a service if not present, else updates it
func CreateOrUpdateService(
	k8sClient client.Client,
	service *v1.Service,
	ownerRef *metav1.OwnerReference,
) error {
	existingService := &v1.Service{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      service.Name,
			Namespace: service.Namespace,
		},
		existingService,
	)
	if errors.IsNotFound(err) {
		logrus.Debugf("Creating %s service", service.Name)
		return k8sClient.Create(context.TODO(), service)
	} else if err != nil {
		return err
	}

	ownerRefs := make([]metav1.OwnerReference, 0)
	if ownerRef != nil {
		ownerRefs = append(ownerRefs, *ownerRef)
		for _, o := range existingService.OwnerReferences {
			if o.UID != ownerRef.UID {
				ownerRefs = append(ownerRefs, o)
			}
		}
	}

	if service.Spec.Type == "" {
		service.Spec.Type = v1.ServiceTypeClusterIP
	}

	modified := existingService.Spec.Type != service.Spec.Type ||
		!reflect.DeepEqual(existingService.Labels, service.Labels) ||
		!reflect.DeepEqual(existingService.Spec.Selector, service.Spec.Selector)

	portMapping := make(map[string]v1.ServicePort)
	for _, port := range service.Spec.Ports {
		portMapping[port.Name] = port
	}

	servicePorts := make([]v1.ServicePort, 0)
	for _, existingPort := range existingService.Spec.Ports {
		newPort, exists := portMapping[existingPort.Name]
		if !exists {
			// The port is no longer present in the new ports list
			modified = true
			continue
		}
		if existingPort.Protocol == "" {
			existingPort.Protocol = v1.ProtocolTCP
		}
		if newPort.Protocol == "" {
			newPort.Protocol = v1.ProtocolTCP
		}
		if existingPort.Port != newPort.Port ||
			!reflect.DeepEqual(existingPort.TargetPort, newPort.TargetPort) ||
			existingPort.Protocol != newPort.Protocol {
			modified = true
		}
		toUpdate := existingPort.DeepCopy()
		toUpdate.Port = newPort.Port
		toUpdate.TargetPort = newPort.TargetPort
		toUpdate.Protocol = newPort.Protocol
		servicePorts = append(servicePorts, *toUpdate)
		delete(portMapping, newPort.Name)
	}

	// Copy new ports that were not present in the service before
	for _, port := range portMapping {
		modified = true
		toUpdate := port.DeepCopy()
		servicePorts = append(servicePorts, *toUpdate)
	}

	if modified || len(ownerRefs) > len(existingService.OwnerReferences) {
		existingService.OwnerReferences = ownerRefs
		existingService.Labels = service.Labels
		existingService.Spec.Selector = service.Spec.Selector
		existingService.Spec.Type = service.Spec.Type
		existingService.Spec.Ports = servicePorts
		logrus.Debugf("Updating %s service", service.Name)
		return k8sClient.Update(context.TODO(), existingService)
	}
	return nil
}

// DeleteService deletes a service if present
func DeleteService(
	k8sClient client.Client,
	name, namespace string,
) error {
	resource := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}

	service := &v1.Service{}
	err := k8sClient.Get(context.TODO(), resource, service)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	logrus.Debugf("Deleting %s service", name)
	return k8sClient.Delete(context.TODO(), service)
}

// CreateOrUpdateDeployment creates a deployment if not present, else updates it
func CreateOrUpdateDeployment(
	k8sClient client.Client,
	deployment *appsv1.Deployment,
	ownerRef *metav1.OwnerReference,
) error {
	existingDeployment := &appsv1.Deployment{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      deployment.Name,
			Namespace: deployment.Namespace,
		},
		existingDeployment,
	)
	if errors.IsNotFound(err) {
		logrus.Debugf("Creating %s deployment", deployment.Name)
		return k8sClient.Create(context.TODO(), deployment)
	} else if err != nil {
		return err
	}

	for _, o := range existingDeployment.OwnerReferences {
		if o.UID != ownerRef.UID {
			deployment.OwnerReferences = append(deployment.OwnerReferences, o)
		}
	}

	logrus.Debugf("Updating %s deployment", deployment.Name)
	return k8sClient.Update(context.TODO(), deployment)
}

// DeleteDeployment deletes a deployment if present
func DeleteDeployment(
	k8sClient client.Client,
	name, namespace string,
) error {
	resource := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}

	deployment := &appsv1.Deployment{}
	err := k8sClient.Get(context.TODO(), resource, deployment)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	logrus.Debugf("Deleting %s deployment", name)
	return k8sClient.Delete(context.TODO(), deployment)
}

// CreateOrUpdateStatefulSet creates a stateful set if not present, else updates it
func CreateOrUpdateStatefulSet(
	k8sClient client.Client,
	ss *appsv1.StatefulSet,
	ownerRef *metav1.OwnerReference,
) error {
	existingSS := &appsv1.StatefulSet{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      ss.Name,
			Namespace: ss.Namespace,
		},
		existingSS,
	)
	if errors.IsNotFound(err) {
		logrus.Debugf("Creating %s stateful set", ss.Name)
		return k8sClient.Create(context.TODO(), ss)
	} else if err != nil {
		return err
	}

	for _, o := range existingSS.OwnerReferences {
		if o.UID != ownerRef.UID {
			ss.OwnerReferences = append(ss.OwnerReferences, o)
		}
	}

	logrus.Debugf("Updating %s stateful set", ss.Name)
	return k8sClient.Update(context.TODO(), ss)
}

// DeleteStatefulSet deletes a stateful set if present
func DeleteStatefulSet(
	k8sClient client.Client,
	name, namespace string,
) error {
	resource := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}

	ss := &appsv1.StatefulSet{}
	err := k8sClient.Get(context.TODO(), resource, ss)
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	logrus.Debugf("Deleting %s stateful set", name)
	return k8sClient.Delete(context.TODO(), ss)
}

// CreateOrUpdateDaemonSet creates a daemon set if not present, else updates it
func CreateOrUpdateDaemonSet(
	k8sClient client.Client,
	ds *appsv1.DaemonSet,
	ownerRef *metav1.OwnerReference,
) error {
	existingDS := &appsv1.DaemonSet{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      ds.Name,
			Namespace: ds.Namespace,
		},
		existingDS,
	)
	if errors.IsNotFound(err) {
		logrus.Debugf("Creating %s daemon set", ds.Name)
		return k8sClient.Create(context.TODO(), ds)
	} else if err != nil {
		return err
	}

	ownerRefPresent := false
	for _, o := range existingDS.OwnerReferences {
		if o.UID == ownerRef.UID {
			ownerRefPresent = true
			break
		}
	}
	if !ownerRefPresent {
		if existingDS.OwnerReferences == nil {
			existingDS.OwnerReferences = make([]metav1.OwnerReference, 0)
		}
		existingDS.OwnerReferences = append(existingDS.OwnerReferences, *ownerRef)
	}

	existingDS.Labels = ds.Labels
	existingDS.Spec = ds.Spec

	logrus.Debugf("Updating %s daemon set", ds.Name)
	return k8sClient.Update(context.TODO(), existingDS)
}

// CreateOrUpdateStorageNodeStatus creates a StorageNodeStatus if not present, else updates it
func CreateOrUpdateStorageNodeStatus(
	k8sClient client.Client,
	sns *corev1alpha1.StorageNodeStatus,
	ownerRef *metav1.OwnerReference,
) error {
	existingSNS := &corev1alpha1.StorageNodeStatus{}
	err := k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      sns.Name,
			Namespace: sns.Namespace,
		},
		existingSNS,
	)
	if errors.IsNotFound(err) {
		logrus.Debugf("Creating StorageNodeStatus %s/%s", sns.Namespace, sns.Name)
		return k8sClient.Create(context.TODO(), sns)
	} else if err != nil {
		return err
	}

	ownerRefs := make([]metav1.OwnerReference, 0)
	if ownerRef != nil {
		ownerRefs = append(ownerRefs, *ownerRef)
		for _, o := range existingSNS.OwnerReferences {
			if o.UID != ownerRef.UID {
				ownerRefs = append(ownerRefs, o)
			}
		}
	}

	modified := !reflect.DeepEqual(sns.Status, existingSNS.Status)

	if modified || len(ownerRefs) > len(existingSNS.OwnerReferences) {
		existingSNS.Status = sns.Status
		logrus.Debugf("Updating StorageNodeStatus %s/%s", sns.Namespace, sns.Name)
		return k8sClient.Status().Update(context.TODO(), existingSNS)
	}
	return nil
}

// GetDaemonSetPods returns a list of pods for the given daemon set
func GetDaemonSetPods(
	k8sClient client.Client,
	ds *appsv1.DaemonSet,
) ([]*v1.Pod, error) {
	return GetPodsByOwner(k8sClient, ds.UID, ds.Namespace)
}

// GetPodsByOwner returns pods for a given owner and namespace
func GetPodsByOwner(
	k8sClient client.Client,
	ownerUID types.UID,
	namespace string,
) ([]*v1.Pod, error) {
	podList := &v1.PodList{}
	err := k8sClient.List(
		context.TODO(),
		&client.ListOptions{
			Namespace: namespace,
		},
		podList,
	)
	if err != nil {
		return nil, err
	}

	result := make([]*v1.Pod, 0)
	for _, pod := range podList.Items {
		for _, owner := range pod.OwnerReferences {
			if owner.UID == ownerUID {
				result = append(result, pod.DeepCopy())
			}
		}
	}
	return result, nil
}
