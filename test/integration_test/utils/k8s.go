package utils

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"reflect"

	appops "github.com/portworx/sched-ops/k8s/apps"
	coreops "github.com/portworx/sched-ops/k8s/core"
	rbacops "github.com/portworx/sched-ops/k8s/rbac"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
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
		} else {
			err = fmt.Errorf("unsupported object: %v", reflect.TypeOf(obj))
		}
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func decodeSpec(specContents []byte) (runtime.Object, error) {
	obj, _, err := scheme.Codecs.UniversalDeserializer().Decode([]byte(specContents), nil, nil)
	if err != nil {
		scheme := runtime.NewScheme()
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
	}
	return nil, fmt.Errorf("unsupported object: %v", reflect.TypeOf(in))
}
