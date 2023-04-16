package portworxdiag

import (
	"context"
	"fmt"
	"strings"
	"time"

	apiextensionsops "github.com/portworx/sched-ops/k8s/apiextensions"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/libopenstorage/operator/drivers/storage"
	portworxv1 "github.com/libopenstorage/operator/pkg/apis/portworx/v1"
	"github.com/libopenstorage/operator/pkg/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
)

const (
	// ControllerName is the name of the controller
	ControllerName      = "portworxdiag-controller"
	validateCRDInterval = 5 * time.Second
	validateCRDTimeout  = 1 * time.Minute
	crdBasePath         = "/crds"
	portworxDiagCRDFile = "portworx.io_portworxdiags.yaml"
)

var _ reconcile.Reconciler = &Controller{}

var (
	crdBaseDir = getCRDBasePath
)

// Controller reconciles a StorageCluster object
type Controller struct {
	client   client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder
	Driver   storage.Driver
	ctrl     controller.Controller
}

// Init initialize the portworx diag controller.
func (c *Controller) Init(mgr manager.Manager) error {
	c.client = mgr.GetClient()
	c.scheme = mgr.GetScheme()
	c.recorder = mgr.GetEventRecorderFor(ControllerName)

	var err error
	// Create a new controller
	c.ctrl, err = controller.New(ControllerName, mgr, controller.Options{Reconciler: c})
	return err
}

// StartWatch starts the watch on the PortworxDiag object type.
func (c *Controller) StartWatch() error {
	if c.ctrl == nil {
		return fmt.Errorf("controller not initialized to start a watch")
	}

	return c.ctrl.Watch(
		&source.Kind{Type: &portworxv1.PortworxDiag{}},
		&handler.EnqueueRequestForObject{},
	)
}

func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logrus.WithFields(map[string]interface{}{
		"Request.Namespace": req.Namespace,
		"Request.Name":      req.Name,
	})
	log.Infof("Reconciling PortworxDiag")

	// Fetch the StorageCluster instance
	diag := &portworxv1.PortworxDiag{}
	err := c.client.Get(context.TODO(), req.NamespacedName, diag)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	if err := c.syncPortworxDiag(diag); err != nil {
		// Ignore object revision conflict errors, as PortworxDiag could have been edited.
		// The next reconcile loop should be able to resolve the issue.
		if strings.Contains(err.Error(), k8s.UpdateRevisionConflictErr) {
			logrus.Warnf("failed to sync PortworxDiag %s: %v", req, err)
			return reconcile.Result{}, nil
		}

		k8s.WarningEvent(c.recorder, diag, util.FailedSyncReason, err.Error())
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (c *Controller) syncPortworxDiag(diag *portworxv1.PortworxDiag) error {
	// TODO: Create diag pods with appropriate parameters to trigger the diag collection.
	return nil
}

// RegisterCRD registers and validates CRDs
func (c *Controller) RegisterCRD() error {
	crd, err := k8s.GetCRDFromFile(portworxDiagCRDFile, crdBaseDir())
	if err != nil {
		return err
	}
	latestCRD, err := apiextensionsops.Instance().GetCRD(crd.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		if err = apiextensionsops.Instance().RegisterCRD(crd); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		crd.ResourceVersion = latestCRD.ResourceVersion
		if _, err := apiextensionsops.Instance().UpdateCRD(crd); err != nil {
			return err
		}
	}

	resource := fmt.Sprintf("%s.%s", crd.Spec.Names.Plural, crd.Spec.Group)
	return apiextensionsops.Instance().ValidateCRD(resource, validateCRDTimeout, validateCRDInterval)
}

func getCRDBasePath() string {
	return crdBasePath
}
