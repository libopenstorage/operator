package utils

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"reflect"
	"time"

	k8sutil "github.com/libopenstorage/operator/pkg/util/k8s"
	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	appops "github.com/portworx/sched-ops/k8s/apps"
	coreops "github.com/portworx/sched-ops/k8s/core"
	prometheusops "github.com/portworx/sched-ops/k8s/prometheus"
	rbacops "github.com/portworx/sched-ops/k8s/rbac"
	storageops "github.com/portworx/sched-ops/k8s/storage"
	"github.com/portworx/sched-ops/task"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ParseSpecs parses the file and returns all the valid k8s objects
func ParseSpecs(filename string) ([]runtime.Object, error) {
	var specs []runtime.Object
	file, err := os.Open(path.Join("testspec", filename))
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	specReader := yaml.NewYAMLReader(reader)

	for {
		specContents, err := specReader.Read()
		if err == io.EOF {
			break
		}

		if len(bytes.TrimSpace(specContents)) > 0 {
			obj, err := decodeSpec(specContents)
			if err != nil {
				logrus.Warnf("Error decoding spec from %v: %v", filename, err)
				return nil, err
			}

			specObj, err := validateSpec(obj)
			if err != nil {
				logrus.Warnf("Error parsing spec from %v: %v", filename, err)
				return nil, err
			}

			specs = append(specs, specObj)
		}
	}
	return specs, nil
}

// CreateObjects creates the given k8s objects
func CreateObjects(objects []runtime.Object) error {
	for _, obj := range objects {
		var err error
		if ns, ok := obj.(*v1.Namespace); ok {
			_, err = coreops.Instance().CreateNamespace(ns)
		} else if ds, ok := obj.(*appsv1.DaemonSet); ok {
			_, err = appops.Instance().CreateDaemonSet(ds, metav1.CreateOptions{})
		} else if dep, ok := obj.(*appsv1.Deployment); ok {
			_, err = appops.Instance().CreateDeployment(dep, metav1.CreateOptions{})
		} else if svc, ok := obj.(*v1.Service); ok {
			_, err = coreops.Instance().CreateService(svc)
		} else if cm, ok := obj.(*v1.ConfigMap); ok {
			_, err = coreops.Instance().CreateConfigMap(cm)
		} else if sa, ok := obj.(*v1.ServiceAccount); ok {
			_, err = coreops.Instance().CreateServiceAccount(sa)
		} else if role, ok := obj.(*rbacv1.Role); ok {
			_, err = rbacops.Instance().CreateRole(role)
		} else if rb, ok := obj.(*rbacv1.RoleBinding); ok {
			_, err = rbacops.Instance().CreateRoleBinding(rb)
		} else if clusterRole, ok := obj.(*rbacv1.ClusterRole); ok {
			_, err = rbacops.Instance().CreateClusterRole(clusterRole)
		} else if crb, ok := obj.(*rbacv1.ClusterRoleBinding); ok {
			_, err = rbacops.Instance().CreateClusterRoleBinding(crb)
		} else if crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition); ok {
			err = apiextensionsops.Instance().RegisterCRD(crd)
			if errors.IsAlreadyExists(err) {
				err = nil
			}
		} else if sc, ok := obj.(*storagev1.StorageClass); ok {
			_, err = storageops.Instance().CreateStorageClass(sc)
		} else if sm, ok := obj.(*monitoringv1.ServiceMonitor); ok {
			_, err = prometheusops.Instance().CreateServiceMonitor(sm)
		} else if pr, ok := obj.(*monitoringv1.PrometheusRule); ok {
			_, err = prometheusops.Instance().CreatePrometheusRule(pr)
		} else {
			err = fmt.Errorf("unsupported object: %v", reflect.TypeOf(obj))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// DeleteObjects deletes the given k8s objects if present
func DeleteObjects(objects []runtime.Object) error {
	for _, obj := range objects {
		var err error
		if ns, ok := obj.(*v1.Namespace); ok {
			err = coreops.Instance().DeleteNamespace(ns.Name)
		} else if ds, ok := obj.(*appsv1.DaemonSet); ok {
			err = appops.Instance().DeleteDaemonSet(ds.Name, ds.Namespace)
		} else if dep, ok := obj.(*appsv1.Deployment); ok {
			err = appops.Instance().DeleteDeployment(dep.Name, dep.Namespace)
		} else if svc, ok := obj.(*v1.Service); ok {
			err = coreops.Instance().DeleteService(svc.Name, svc.Namespace)
		} else if cm, ok := obj.(*v1.ConfigMap); ok {
			err = coreops.Instance().DeleteConfigMap(cm.Name, cm.Namespace)
		} else if sa, ok := obj.(*v1.ServiceAccount); ok {
			err = coreops.Instance().DeleteServiceAccount(sa.Name, sa.Namespace)
		} else if role, ok := obj.(*rbacv1.Role); ok {
			err = rbacops.Instance().DeleteRole(role.Name, role.Namespace)
		} else if rb, ok := obj.(*rbacv1.RoleBinding); ok {
			err = rbacops.Instance().DeleteRoleBinding(rb.Name, rb.Namespace)
		} else if clusterRole, ok := obj.(*rbacv1.ClusterRole); ok {
			err = rbacops.Instance().DeleteClusterRole(clusterRole.Name)
		} else if crb, ok := obj.(*rbacv1.ClusterRoleBinding); ok {
			err = rbacops.Instance().DeleteClusterRoleBinding(crb.Name)
		} else if crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition); ok {
			crdName := fmt.Sprintf("%s.%s", crd.Spec.Names.Plural, crd.Spec.Group)
			err = apiextensionsops.Instance().DeleteCRD(crdName)
		} else if sc, ok := obj.(*storagev1.StorageClass); ok {
			err = storageops.Instance().DeleteStorageClass(sc.Name)
		} else if sm, ok := obj.(*monitoringv1.ServiceMonitor); ok {
			err = prometheusops.Instance().DeleteServiceMonitor(sm.Name, sm.Namespace)
		} else if pr, ok := obj.(*monitoringv1.PrometheusRule); ok {
			err = prometheusops.Instance().DeletePrometheusRule(pr.Name, pr.Namespace)
		} else {
			err = fmt.Errorf("unsupported object: %v", reflect.TypeOf(obj))
		}
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// ValidateObjectsAreTerminated validates that the objects have been terminated.
// The skip flag will not validate termination of certain objects.
func ValidateObjectsAreTerminated(objects []runtime.Object, skip bool) error {
	s := scheme.Scheme
	apiextensionsv1.AddToScheme(s)
	monitoringv1.AddToScheme(s)
	k8sClient, err := k8sutil.NewK8sClient(s)
	if err != nil {
		return err
	}
	for _, obj := range objects {
		var err error
		if ns, ok := obj.(*v1.Namespace); ok {
			err = validateObjectIsTerminated(k8sClient, ns.Name, "", ns, skip)
		} else if ds, ok := obj.(*appsv1.DaemonSet); ok {
			err = validateObjectIsTerminated(k8sClient, ds.Name, ds.Namespace, ds, false)
		} else if dep, ok := obj.(*appsv1.Deployment); ok {
			err = validateObjectIsTerminated(k8sClient, dep.Name, dep.Namespace, dep, false)
		} else if svc, ok := obj.(*v1.Service); ok {
			err = validateObjectIsTerminated(k8sClient, svc.Name, svc.Namespace, svc, false)
		} else if cm, ok := obj.(*v1.ConfigMap); ok {
			err = validateObjectIsTerminated(k8sClient, cm.Name, cm.Namespace, cm, false)
		} else if sa, ok := obj.(*v1.ServiceAccount); ok {
			err = validateObjectIsTerminated(k8sClient, sa.Name, sa.Namespace, sa, false)
		} else if role, ok := obj.(*rbacv1.Role); ok {
			err = validateObjectIsTerminated(k8sClient, role.Name, role.Namespace, role, false)
		} else if rb, ok := obj.(*rbacv1.RoleBinding); ok {
			err = validateObjectIsTerminated(k8sClient, rb.Name, rb.Namespace, rb, false)
		} else if clusterRole, ok := obj.(*rbacv1.ClusterRole); ok {
			err = validateObjectIsTerminated(k8sClient, clusterRole.Name, "", clusterRole, false)
		} else if crb, ok := obj.(*rbacv1.ClusterRoleBinding); ok {
			err = validateObjectIsTerminated(k8sClient, crb.Name, "", crb, false)
		} else if crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition); ok {
			crdName := fmt.Sprintf("%s.%s", crd.Spec.Names.Plural, crd.Spec.Group)
			err = validateObjectIsTerminated(k8sClient, crdName, "", crd, skip)
		} else if sc, ok := obj.(*storagev1.StorageClass); ok {
			err = validateObjectIsTerminated(k8sClient, sc.Name, "", sc, skip)
		} else if sm, ok := obj.(*monitoringv1.ServiceMonitor); ok {
			err = validateObjectIsTerminated(k8sClient, sm.Name, sm.Namespace, sm, false)
		} else if pr, ok := obj.(*monitoringv1.PrometheusRule); ok {
			err = validateObjectIsTerminated(k8sClient, pr.Name, pr.Namespace, pr, false)
		} else {
			err = fmt.Errorf("unsupported object: %v", reflect.TypeOf(obj))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func validateObjectIsTerminated(
	k8sClient client.Client,
	name, namespace string,
	obj client.Object,
	skip bool,
) error {
	if skip {
		return nil
	}

	kind := obj.GetObjectKind().GroupVersionKind().Kind
	t := func() (interface{}, bool, error) {
		objName := types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}
		err := k8sClient.Get(context.TODO(), objName, obj)
		if errors.IsNotFound(err) {
			return nil, false, nil
		} else if err != nil {
			return nil, true, err
		}
		return nil, true, fmt.Errorf("%s %s is still present", kind, objName.String())
	}

	if _, err := task.DoRetryWithTimeout(t, 1*time.Minute, 2*time.Second); err != nil {
		return err
	}
	return nil
}

func decodeSpec(specContents []byte) (runtime.Object, error) {
	obj, _, err := scheme.Codecs.UniversalDeserializer().Decode([]byte(specContents), nil, nil)
	if err != nil {
		scheme := runtime.NewScheme()
		apiextensionsv1.AddToScheme(scheme)
		monitoringv1.AddToScheme(scheme)
		codecs := serializer.NewCodecFactory(scheme)
		obj, _, err = codecs.UniversalDeserializer().Decode([]byte(specContents), nil, nil)
		if err != nil {
			return nil, err
		}
	}
	return obj, nil
}

func validateSpec(in interface{}) (runtime.Object, error) {
	if specObj, ok := in.(*v1.Namespace); ok {
		return specObj, nil
	} else if specObj, ok := in.(*appsv1.DaemonSet); ok {
		return specObj, nil
	} else if specObj, ok := in.(*appsv1.Deployment); ok {
		return specObj, nil
	} else if specObj, ok := in.(*v1.Service); ok {
		return specObj, nil
	} else if specObj, ok := in.(*v1.ConfigMap); ok {
		return specObj, nil
	} else if specObj, ok := in.(*v1.ServiceAccount); ok {
		return specObj, nil
	} else if specObj, ok := in.(*rbacv1.Role); ok {
		return specObj, nil
	} else if specObj, ok := in.(*rbacv1.RoleBinding); ok {
		return specObj, nil
	} else if specObj, ok := in.(*rbacv1.ClusterRole); ok {
		return specObj, nil
	} else if specObj, ok := in.(*rbacv1.ClusterRoleBinding); ok {
		return specObj, nil
	} else if specObj, ok := in.(*apiextensionsv1.CustomResourceDefinition); ok {
		return specObj, nil
	} else if specObj, ok := in.(*storagev1.StorageClass); ok {
		return specObj, nil
	} else if specObj, ok := in.(*monitoringv1.ServiceMonitor); ok {
		return specObj, nil
	} else if specObj, ok := in.(*monitoringv1.PrometheusRule); ok {
		return specObj, nil
	}
	return nil, fmt.Errorf("unsupported object: %v", reflect.TypeOf(in))
}
