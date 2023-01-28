package csr

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/hashicorp/go-version"
	"github.com/libopenstorage/operator/drivers/storage/portworx/util"
	"github.com/libopenstorage/operator/pkg/util/k8s"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	certv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	ControllerName    = "px-csr-controller"
	labelNameKey      = "name"
	labelNameVal      = "portworx"
	labelClusterIDKey = "cid"
)

var _ reconcile.Reconciler = &Controller{}

var (
	reMatchUserAnyNamespace   = regexp.MustCompile("system:serviceaccount:[^:]+:portworx")
	minSupportedK8sVersion, _ = version.NewVersion("1.19.0")
	registration              sync.Map
)

type Controller struct {
	client   client.Client
	scheme   *runtime.Scheme
	recorder record.EventRecorder
	ctrl     controller.Controller
}

// Init initializes CSR controller
func (c *Controller) Init(mgr manager.Manager) error {
	if ver, err := k8s.GetVersion(); err != nil {
		return fmt.Errorf("could not determine K8s version: %s", err)
	} else if ver.LessThan(minSupportedK8sVersion) {
		logrus.Warnf("CSR auto-approve not supported on K8s version %s", ver)
		c.ctrl = nil
		c.client = nil
		return nil
	}

	c.client = mgr.GetClient()
	c.scheme = mgr.GetScheme()
	c.recorder = mgr.GetEventRecorderFor(ControllerName)

	var err error
	// Create a new controller
	c.ctrl, err = controller.New(ControllerName, mgr, controller.Options{Reconciler: c})
	if err != nil {
		return err
	}

	return nil
}

// StartWatch starts the watch on CertificateSigningRequests
func (c *Controller) StartWatch() error {
	if c.ctrl == nil || c.client == nil {
		logrus.Warn("CSR.StartWatch() bailing out  (not initialized)")
		return nil
	}

	err := c.ctrl.Watch(
		&source.Kind{Type: &certv1.CertificateSigningRequest{}},
		&handler.EnqueueRequestForObject{},
	)
	if err != nil {
		return err
	}

	return nil
}

// Reconcile will automatically approve Portworx-requested certificates
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var (
		csr certv1.CertificateSigningRequest
		ret reconcile.Result
		log = logrus.WithField("Request.Name", req.Name)
	)

	if !util.IsTLSEnabled() {
		log.Debugf("CSR.Reconcile() bailing out  (TLS not enabled)")
		return ret, nil
	}

	log.Infof("Reconciling CSR")

	if err := c.client.Get(ctx, req.NamespacedName, &csr); err != nil {
		if apierrors.IsNotFound(err) {
			log.Debugf("Ignoring deleted CSR request")
			return ret, nil
		}
		log.WithError(err).Errorf("Unable to get CSR")
		return ret, err
	}

	// ignore spec.signerName != kubernetes.io/kubelet-serving
	if csr.Spec.SignerName != certv1.KubeletServingSignerName {
		log.Debugf("Ignoring CSR with signer %s", csr.Spec.SignerName)
		return ret, nil
	}

	// ignore names which don't start with "px-"
	if !strings.HasPrefix(csr.GetName(), "px-") {
		log.Debugf("Ignoring CSR  (not px name)")
		return ret, nil
	}

	// ignore spec.username != system:serviceaccount:<namespace>:portworx
	if !reMatchUserAnyNamespace.MatchString(csr.Spec.Username) {
		log.Debugf("Ignoring CSR  (wrong user %s)", csr.Spec.Username)
		return ret, nil
	}

	if approved, denied := c.getCertApprovalCondition(&csr.Status); approved || denied {
		log.WithField("approved", approved).WithField("denied", denied).
			Debugf("CSR already approved/denied")
		return ret, nil
	}

	if cnt := len(csr.Status.Certificate); cnt > 0 {
		log.Debugf("CSR Certificate already signed  (%d certs attached)", cnt)
		return ret, nil
	}

	// ignore no labels, or missing label name=portworx, or missing label cid
	var clusterID string
	if labs := csr.GetLabels(); len(labs) == 0 {
		log.Debugf("Ignoring CSR  (no labels)")
		return ret, nil
	} else if labs[labelNameKey] != labelNameVal {
		log.Debugf("Ignoring CSR  (no label %s=%s)", labelNameKey, labelNameVal)
		return ret, nil
	} else if clusterID = labs[labelClusterIDKey]; len(clusterID) <= 0 {
		log.Debugf("Ignoring CSR  (no label %s)", labelClusterIDKey)
		return ret, nil
	}

	// check registration, requeue if not (yet) registered, otherwise approve or ignore
	isApprove, has := registration.Load(clusterID)
	if !has {
		log.Debugf("Postponing CSR eval  (clusterID %s not registered)", clusterID)
		return ctrl.Result{Requeue: true}, nil
	} else if !isApprove.(bool) {
		log.WithField("clusterID", clusterID).
			Warnf("NOT auto-approving CSR -- approve manually via `kubectl certificate approve %s`)", req.Name)
		return ret, nil
	}

	// CHECKME: should we inspect CSR (e.g. check DNS names + IPs, expiration)

	log.WithField("clusterID", clusterID).Info("Approving CSR")
	c.appendCondition(&csr, true, "")

	_, err := coreops.Instance().CertificateSigningRequestsUpdateApproval(req.Name, &csr)
	if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
		// The CSR has been updated or deleted since we read it.
		// Requeue the CSR to try to reconcile again.
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.WithError(err).Errorf("Error approving CSR")
		return ret, err
	}

	return ret, nil
}

// getCertApprovalCondition scans CSR status, returns <approved> and <denied>
func (c *Controller) getCertApprovalCondition(st *certv1.CertificateSigningRequestStatus) (approved, denied bool) {
	for _, cond := range st.Conditions {
		switch cond.Type {
		case certv1.CertificateApproved:
			approved = true
		case certv1.CertificateDenied:
			denied = true
		}
	}
	return
}

// appendCondition updates CSR status as approved or denied
func (c *Controller) appendCondition(csr *certv1.CertificateSigningRequest, approved bool, reason string) {
	cnd := certv1.CertificateSigningRequestCondition{
		Type:               certv1.CertificateDenied,
		Status:             corev1.ConditionTrue,
		Reason:             "cert denied by px-operator",
		Message:            "CSR failed px-operator validation process. Reason: " + reason,
		LastUpdateTime:     metav1.Now(),
		LastTransitionTime: metav1.Time{},
	}
	if approved {
		cnd.Type = certv1.CertificateApproved
		cnd.Reason = "cert validated by px-operator"
		cnd.Message = "CSR approved by px-operator"
	}
	csr.Status.Conditions = append(csr.Status.Conditions, cnd)
}

// RegisterAutoApproval is used by the storagecluster.Reconcile, to enable (or disable) auto-approval
// of the CSRs, based on the ClusterID
func RegisterAutoApproval(clusterID string, autoApprove bool) {
	registration.Store(clusterID, autoApprove)
}
