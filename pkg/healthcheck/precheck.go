package healthcheck

import (
	"fmt"
	"time"

	operatorcorev1 "github.com/libopenstorage/operator/pkg/apis/core/v1"
	k8schecks "github.com/libopenstorage/operator/pkg/healthcheck/checks/kubernetes"
	k8shcchecks "github.com/libopenstorage/operator/px/px-health-check/pkg/checks/kubernetes"
	"github.com/libopenstorage/operator/px/px-health-check/pkg/healthcheck"
	"github.com/libopenstorage/operator/px/px-health-check/pkg/states"

	log "github.com/sirupsen/logrus"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Precheck runs tests when the operator is first installed. These tests
// are to make sure that the operator will run successfully.
func Precheck(config *rest.Config) error {

	// Required checks
	categories := []*healthcheck.Category{
		RequiredHealthChecks(),
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("unable to create kubernetes API clientset: %v", err)
	}

	// Create a healthchecker
	hc := healthcheck.NewHealthChecker(categories).
		WithContextTimeout(time.Second * 10)
	states.WithKubernetesClientSet(hc.State(), clientset)
	reporter := hc.Run()

	if !reporter.Successful() {
		bytes, err := reporter.ToJSON()
		if err != nil {
			log.Errorf("Unable to get health check results in JSON: %v", err)
		} else {
			log.Errorf("Results(JSON): %s", string(bytes))
		}
		return fmt.Errorf("installation health checks failed")
	} else {
		log.Info("installation health checks passed")
	}

	return nil
}

func RequiredHealthChecks() *healthcheck.Category {

	checks := []*healthcheck.Checker{
		k8shcchecks.RequiredKubernetesVersion(),
	}

	return healthcheck.NewCategory("required", checks, true, healthcheck.PortworxDefaultHintBaseUrl)
}

func GatherFactsHealthChecks(
	k8sclient client.Client,
	cluster *operatorcorev1.StorageCluster,
) *healthcheck.Category {
	checks := []*healthcheck.Checker{
		k8schecks.GatherKubernesNodes(k8sclient, cluster),
	}

	return healthcheck.NewCategory("gather facts", checks, true, healthcheck.PortworxDefaultHintBaseUrl)
}
