package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestServicePortAddition(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:     "p1",
					Port:     int32(1000),
					Protocol: v1.ProtocolTCP,
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// New port added to the target service spec
	expectedService.Spec.Ports = append(
		expectedService.Spec.Ports,
		v1.ServicePort{Name: "p2", Port: int32(2000), Protocol: v1.ProtocolTCP},
	)

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)
}

func TestServicePortRemoval(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:     "p1",
					Port:     int32(1000),
					Protocol: v1.ProtocolTCP,
				},
				{
					Name:     "p2",
					Port:     int32(2000),
					Protocol: v1.ProtocolTCP,
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Remove port from the target service spec
	expectedService.Spec.Ports = append([]v1.ServicePort{}, expectedService.Spec.Ports[1:]...)

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)
}

func TestServiceTargetPortChange(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:       "p1",
					Port:       int32(1000),
					TargetPort: intstr.FromInt(1000),
					Protocol:   v1.ProtocolTCP,
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Change the target port number of an existing port
	expectedService.Spec.Ports[0].TargetPort = intstr.FromInt(2000)

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)
}

func TestServicePortNumberChange(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:     "p1",
					Port:     int32(1000),
					Protocol: v1.ProtocolTCP,
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Change the port number of an existing port
	expectedService.Spec.Ports[0].Port = int32(2000)

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)
}

func TestServicePortProtocolChange(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:     "p1",
					Port:     int32(1000),
					Protocol: v1.ProtocolTCP,
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Change the protocol of an existing port
	expectedService.Spec.Ports[0].Protocol = v1.ProtocolUDP

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)
}

func TestServicePortEmptyExistingProtocol(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name: "p1",
					Port: int32(1000),
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Set the default TCP protocol and nothing should change
	expectedService.Spec.Ports[0].Protocol = v1.ProtocolTCP

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Len(t, expectedService.Spec.Ports, 1)
	require.Empty(t, actualService.Spec.Ports[0].Protocol)
}

func TestServicePortEmptyNewProtocol(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name:     "p1",
					Port:     int32(1000),
					Protocol: v1.ProtocolTCP,
				},
			},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, expectedService.Spec.Ports, actualService.Spec.Ports)

	// Set the protocol to empty and nothing should change as default is TCP
	expectedService.Spec.Ports[0].Protocol = ""

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Len(t, expectedService.Spec.Ports, 1)
	require.Equal(t, v1.ProtocolTCP, actualService.Spec.Ports[0].Protocol)
}

func TestServiceChangeServiceType(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeLoadBalancer, actualService.Spec.Type)

	// Change service type
	expectedService.Spec.Type = v1.ServiceTypeNodePort

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, v1.ServiceTypeNodePort, actualService.Spec.Type)
}

func TestServiceChangeLabels(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Empty(t, actualService.Labels)

	// Add new labels
	expectedService.Labels = map[string]string{"key": "value"}

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, expectedService.Labels, actualService.Labels)

	// Change labels
	expectedService.Labels = map[string]string{"key": "newvalue"}

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, expectedService.Labels, actualService.Labels)

	// Remove labels
	expectedService.Labels = nil

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Empty(t, actualService.Labels)
}

func TestServiceWithOwnerReferences(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	firstOwner := metav1.OwnerReference{UID: "first-owner"}
	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test",
			Namespace:       "test-ns",
			OwnerReferences: []metav1.OwnerReference{firstOwner},
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualService.OwnerReferences)

	// Update with the same owner. Nothing should change as owner hasn't changed.
	err = CreateOrUpdateService(k8sClient, expectedService, &firstOwner)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{firstOwner}, actualService.OwnerReferences)

	// Update with a new owner.
	secondOwner := metav1.OwnerReference{UID: "second-owner"}

	err = CreateOrUpdateService(k8sClient, expectedService, &secondOwner)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.ElementsMatch(t, []metav1.OwnerReference{secondOwner, firstOwner}, actualService.OwnerReferences)
}

func TestServiceChangeSelector(t *testing.T) {
	k8sClient := fake.NewFakeClient()

	expectedService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "test-ns",
		},
	}

	err := CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService := &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Empty(t, actualService.Spec.Selector)

	// Add new selectors
	expectedService.Spec.Selector = map[string]string{"key": "value"}

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, expectedService.Spec.Selector, actualService.Spec.Selector)

	// Change selectors
	expectedService.Spec.Selector = map[string]string{"key": "newvalue"}

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Equal(t, expectedService.Spec.Selector, actualService.Spec.Selector)

	// Remove selectors
	expectedService.Spec.Selector = nil

	err = CreateOrUpdateService(k8sClient, expectedService, nil)
	require.NoError(t, err)

	actualService = &v1.Service{}
	err = get(k8sClient, actualService, "test", "test-ns")
	require.NoError(t, err)
	require.Empty(t, actualService.Spec.Selector)
}

func get(k8sClient client.Client, obj runtime.Object, name, namespace string) error {
	return k8sClient.Get(
		context.TODO(),
		types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
		obj,
	)
}
