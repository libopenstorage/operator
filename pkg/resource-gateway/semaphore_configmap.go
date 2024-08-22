package resource_gateway

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	configMapActiveLeasesKey = "activeLeases"
	configMapNPermitsKey     = "nPermits"
	configMapLeaseTimeoutKey = "leaseTimeout"
)

type configMap struct {
	cm *corev1.ConfigMap
}

// create the configmap if it doesn't exist then, fetch the latest copy of configmap and,
// update semaphore config values (nPermits, leaseTimeout)
func createConfigMap(config *SemaphoreConfig) *configMap {

	remoteConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.ConfigMapName,
			Namespace: config.ConfigMapNamespace,
			Labels:    config.ConfigMapLabels,
		},
		Data: map[string]string{
			configMapActiveLeasesKey: "",
			configMapNPermitsKey:     fmt.Sprintf("%d", config.NPermits),
			configMapLeaseTimeoutKey: config.LeaseTimeout.String(),
		},
	}

	_, err := core.Instance().CreateConfigMap(remoteConfigMap)
	if err != nil && !k8s_errors.IsAlreadyExists(err) {
		panic(fmt.Sprintf("Failed to create configmap %s in namespace %s: %v", config.ConfigMapName, config.ConfigMapNamespace, err))
	}

	return &configMap{
		cm: remoteConfigMap,
	}
}

func (c *configMap) Name() string {
	return c.cm.Name
}

func (c *configMap) Namespace() string {
	return c.cm.Namespace
}

func (c *configMap) Labels() map[string]string {
	return c.cm.Labels
}

func (c *configMap) NPermits() (uint32, error) {
	nPermits, err := strconv.Atoi(c.cm.Data[configMapNPermitsKey])
	if err != nil {
		return 0, err
	}
	return uint32(nPermits), nil
}

func (c *configMap) LeaseTimeout() (time.Duration, error) {
	leaseTimeout, err := time.ParseDuration(c.cm.Data[configMapLeaseTimeoutKey])
	if err != nil {
		return 0, err
	}
	return leaseTimeout, nil
}

func (c *configMap) ActiveLeases() (map[string]activeLease, error) {
	leases := map[string]activeLease{}
	configMapActiveLeasesValue := c.cm.Data[configMapActiveLeasesKey]
	if configMapActiveLeasesValue == "" {
		return leases, nil
	}
	if err := json.Unmarshal([]byte(configMapActiveLeasesValue), &leases); err != nil {
		return nil, err
	}
	return leases, nil
}

// isConfigMapUpdateRequired compares two maps and returns true if they are different
func isConfigMapUpdateRequired(map1, map2 map[string]activeLease) bool {
	if len(map1) != len(map2) {
		return true
	}
	for key1, val1 := range map1 {
		// TODO how are two structs compared
		if val2, ok := map2[key1]; !ok || val1 != val2 {
			return true
		}
	}
	return false
}

// Update replaces active leases in the configmap with the provided active leases
// it only makes an update call if the active leases have changed
func (c *configMap) Update(newActiveLeases map[string]activeLease) {
	currentActiveLeases, err := c.ActiveLeases()
	if err != nil {
		panic(err)
	}
	if !isConfigMapUpdateRequired(newActiveLeases, currentActiveLeases) {
		return
	}

	leases := map[string]string{}
	for clientId, lease := range newActiveLeases {
		leases[clientId] = fmt.Sprintf("%d", lease.PermitId)
	}
	logrus.Infof("Updating configmap: %v", leases)

	configMapActiveLeasesValue, err := json.Marshal(newActiveLeases)
	if err != nil {
		panic(err)
	}
	c.cm.Data[configMapActiveLeasesKey] = string(configMapActiveLeasesValue)

	c.cm, err = core.Instance().UpdateConfigMap(c.cm)
	if err != nil {
		panic(err)
	}
}
