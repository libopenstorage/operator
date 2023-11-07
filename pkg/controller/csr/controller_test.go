package csr

import (
	"context"
	"os"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/libopenstorage/operator/drivers/storage/portworx/util"
	"github.com/libopenstorage/operator/pkg/client/clientset/versioned/scheme"
	"github.com/libopenstorage/operator/pkg/mock"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	certv1 "k8s.io/api/certificates/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestInit(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	fakeClient := fakek8sclient.NewSimpleClientset()
	k8sClient := testutil.FakeK8sClient()
	coreops.SetInstance(coreops.New(fakeClient))
	recorder := record.NewFakeRecorder(10)

	mgr := mock.NewMockManager(mockCtrl)
	mgr.EXPECT().GetClient().Return(k8sClient).AnyTimes()
	mgr.EXPECT().GetScheme().Return(scheme.Scheme).AnyTimes()
	mgr.EXPECT().GetEventRecorderFor(gomock.Any()).Return(recorder).AnyTimes()
	mgr.EXPECT().Add(gomock.Any()).Return(nil).AnyTimes()
	mgr.EXPECT().Add(gomock.Any()).Return(nil).AnyTimes()
	mgr.EXPECT().GetLogger().Return(log.Log.WithName("test")).AnyTimes()

	versionClient := fakek8sclient.NewSimpleClientset()
	coreops.SetInstance(coreops.New(versionClient))
	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.16.0",
	}

	controller := Controller{
		client:   k8sClient,
		recorder: recorder,
	}

	// Test: Init and Watch on old Kubernetes

	err := controller.Init(mgr)
	require.NoError(t, err)

	ctrl := mock.NewMockController(mockCtrl)
	controller.ctrl = ctrl
	ctrl.EXPECT().Watch(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	err = controller.StartWatch()
	require.NoError(t, err)

	// Test: Init and Watch on new Kubernetes

	versionClient.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{
		GitVersion: "v1.25.1",
	}

	err = controller.Init(mgr)
	require.NoError(t, err)

	err = controller.StartWatch()
	require.NoError(t, err)
}

func TestReconcile(t *testing.T) {
	ctx := context.TODO()
	testCSR := testutil.GetExpectedCSR(t, "csr_approved.yaml")
	require.NotEmpty(t, testCSR)

	k8sClient := testutil.FakeK8sClient(testCSR)
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:   k8sClient,
		recorder: recorder,
	}
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset()))

	// Test: Reconcile should exit immediately if TLS not set
	os.Unsetenv(util.EnvKeyPortworxEnableTLS)
	result, err := controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testCSR.Name,
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Len(t, recorder.Events, 0)

	// rest of the tests will have TLS enabled
	os.Setenv(util.EnvKeyPortworxEnableTLS, "true")
	defer os.Unsetenv(util.EnvKeyPortworxEnableTLS)

	// Test: Reconcile for non-existent CSR  (i.e. reconcile triggered, but CSR deleted)
	result, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: "dummy-unregistered-testCSR",
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Len(t, recorder.Events, 0)

	// Test: Reconcile for invalid non-Kubernetes signer
	testCSR = testutil.GetExpectedCSR(t, "csr_approved.yaml")
	testCSR.Spec.SignerName = "foobar-123"
	controller.client = testutil.FakeK8sClient(testCSR)
	result, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testCSR.Name,
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Len(t, recorder.Events, 0)

	// Test: Reconcile for non-PX CSR
	testCSR = testutil.GetExpectedCSR(t, "csr_approved.yaml")
	testCSR.Name = "foobar-123"
	controller.client = testutil.FakeK8sClient(testCSR)
	result, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testCSR.Name,
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Len(t, recorder.Events, 0)

	// Test: Reconcile for non-PX "requestor" CSR
	testCSR = testutil.GetExpectedCSR(t, "csr_approved.yaml")
	testCSR.Spec.Username = "foobar-123"
	controller.client = testutil.FakeK8sClient(testCSR)
	result, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testCSR.Name,
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Len(t, recorder.Events, 0)

	// Test: Reconcile for already approved CSR  (default `csr_approved.yaml`)
	testCSR = testutil.GetExpectedCSR(t, "csr_approved.yaml")
	controller.client = testutil.FakeK8sClient(testCSR)
	result, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testCSR.Name,
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Len(t, recorder.Events, 0)

	// Test: Reconcile for CSR w/ cert attached
	testCSR = testutil.GetExpectedCSR(t, "csr_approved.yaml")
	testCSR.Status.Conditions = nil
	controller.client = testutil.FakeK8sClient(testCSR)
	result, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testCSR.Name,
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Len(t, recorder.Events, 0)

	// Test: Reconcile for CSR w/ no labels
	testCSR = testutil.GetExpectedCSR(t, "csr_approved.yaml")
	testCSR.Status = certv1.CertificateSigningRequestStatus{}
	testCSR.Labels = nil
	controller.client = testutil.FakeK8sClient(testCSR)
	result, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testCSR.Name,
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Len(t, recorder.Events, 0)

	// Test: Reconcile for CSR w/ no name=portworx label
	testCSR = testutil.GetExpectedCSR(t, "csr_approved.yaml")
	testCSR.Status = certv1.CertificateSigningRequestStatus{}
	testCSR.Labels["name"] = "foobar"
	controller.client = testutil.FakeK8sClient(testCSR)
	result, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testCSR.Name,
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Len(t, recorder.Events, 0)

	// Test: Reconcile for CSR w/ no cid label
	testCSR = testutil.GetExpectedCSR(t, "csr_approved.yaml")
	testCSR.Status = certv1.CertificateSigningRequestStatus{}
	delete(testCSR.Labels, "cid")
	controller.client = testutil.FakeK8sClient(testCSR)
	result, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testCSR.Name,
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Len(t, recorder.Events, 0)

	// Test: Reconcile for CSR w/ non-registered ClusterID
	testCSR = testutil.GetExpectedCSR(t, "csr_approved.yaml")
	testCSR.Status = certv1.CertificateSigningRequestStatus{}
	controller.client = testutil.FakeK8sClient(testCSR)
	result, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testCSR.Name,
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, result)
	assert.True(t, result.Requeue)
	assert.Len(t, recorder.Events, 0)

	// Test: Reconcile for CSR w/ auto-deny ClusterID
	RegisterAutoApproval(testCSR.Labels["cid"], false)
	result, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testCSR.Name,
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Len(t, recorder.Events, 0)

	// Test: Reconcile for CSR w/ auto-approve ClusterID, but update returns NotFound
	RegisterAutoApproval(testCSR.Labels["cid"], true)
	result, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testCSR.Name,
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, result)
	assert.True(t, result.Requeue)
	assert.Len(t, recorder.Events, 0)

	// Test: Reconcile for CSR w/ auto-approve ClusterID
	RegisterAutoApproval(testCSR.Labels["cid"], true)
	coreops.SetInstance(coreops.New(fakek8sclient.NewSimpleClientset(testCSR)))
	result, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: testCSR.Name,
		},
	})
	require.NoError(t, err)
	assert.Empty(t, result)
	assert.Len(t, recorder.Events, 0)

}
