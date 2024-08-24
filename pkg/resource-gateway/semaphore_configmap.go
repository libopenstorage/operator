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
	activeLeasesKey          = "activeLeases"
	nPermitsKey              = "nPermits"
	configMapUpdatePeriodKey = "configMapUpdatePeriod"
	leaseTimeoutKey          = "leaseTimeout"
	deadClientTimeoutKey     = "deadClientTimeout"
	maxQueueSizeKey          = "maxQueueSize"
)

type configMap struct {
	cm *corev1.ConfigMap
}

func createOrUpdateConfigMap(config *SemaphoreConfig) (*configMap, error) {
	remoteConfigMap, err := updateConfigMap(config)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return nil, err
	}

	if k8s_errors.IsNotFound(err) {
		// create a new configmap
		remoteConfigMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      config.ConfigMapName,
				Namespace: config.ConfigMapNamespace,
				Labels:    config.ConfigMapLabels,
			},
			Data: map[string]string{
				activeLeasesKey:          "",
				nPermitsKey:              fmt.Sprintf("%d", config.NPermits),
				configMapUpdatePeriodKey: config.ConfigMapUpdatePeriod.String(),
				leaseTimeoutKey:          config.LeaseTimeout.String(),
				deadClientTimeoutKey:     config.DeadClientTimeout.String(),
				maxQueueSizeKey:          fmt.Sprintf("%d", config.MaxQueueSize),
			},
		}

		remoteConfigMap, err = core.Instance().CreateConfigMap(remoteConfigMap)
		if err != nil && !k8s_errors.IsAlreadyExists(err) {
			return nil, err
		}
	}

	cm := &configMap{
		cm: remoteConfigMap,
	}
	return cm, nil
}

func updateRemoteConfigMap(remoteConfigMap *corev1.ConfigMap, config *SemaphoreConfig) {
	// update the configmap with the latest values
	remoteConfigMap.Data[nPermitsKey] = fmt.Sprintf("%d", config.NPermits)
	remoteConfigMap.Data[configMapUpdatePeriodKey] = config.ConfigMapUpdatePeriod.String()
	remoteConfigMap.Data[leaseTimeoutKey] = config.LeaseTimeout.String()
	remoteConfigMap.Data[deadClientTimeoutKey] = config.DeadClientTimeout.String()
	remoteConfigMap.Data[maxQueueSizeKey] = strconv.FormatUint(uint64(config.MaxQueueSize), 10)
}

// create the configmap if it doesn't exist then, fetch the latest copy of configmap and,
// update semaphore config values (nPermits, leaseTimeout)
func updateConfigMap(config *SemaphoreConfig) (*corev1.ConfigMap, error) {
	remoteConfigMap, err := core.Instance().GetConfigMap(config.ConfigMapName, config.ConfigMapNamespace)
	if err != nil {
		return nil, err
	}

	// update the configmap with the latest values
	updateRemoteConfigMap(remoteConfigMap, config)
	remoteConfigMap, err = core.Instance().UpdateConfigMap(remoteConfigMap)
	if err != nil {
		return nil, err
	}
	return remoteConfigMap, nil
}

func getConfigMap(config *SemaphoreConfig) (*configMap, error) {
	remoteConfigMap, err := core.Instance().GetConfigMap(config.ConfigMapName, config.ConfigMapNamespace)
	if err != nil {
		return nil, err
	}

	configMapUpdatePeriod, err := time.ParseDuration(remoteConfigMap.Data[configMapUpdatePeriodKey])
	if err != nil {
		return nil, err
	}

	leaseTimeout, err := time.ParseDuration(remoteConfigMap.Data[leaseTimeoutKey])
	if err != nil {
		return nil, err
	}

	deadClientTimeout, err := time.ParseDuration(remoteConfigMap.Data[deadClientTimeoutKey])
	if err != nil {
		return nil, err
	}

	maxQueueSize, err := strconv.ParseUint(remoteConfigMap.Data[maxQueueSizeKey], 10, 32)
	if err != nil {
		return nil, err
	}

	config.ConfigMapLabels = remoteConfigMap.Labels
	config.ConfigMapUpdatePeriod = configMapUpdatePeriod
	config.LeaseTimeout = leaseTimeout
	config.DeadClientTimeout = deadClientTimeout
	config.MaxQueueSize = uint(maxQueueSize)

	cm := &configMap{
		cm: remoteConfigMap,
	}
	return cm, nil
}

func (c *configMap) update(config *SemaphoreConfig) error {
	updateRemoteConfigMap(c.cm, config)
	remoteConfigMap, err := core.Instance().UpdateConfigMap(c.cm)
	if err != nil {
		return err
	}
	c.cm = remoteConfigMap
	return nil
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
	nPermits, err := strconv.Atoi(c.cm.Data[nPermitsKey])
	if err != nil {
		return 0, err
	}
	return uint32(nPermits), nil
}

func (c *configMap) LeaseTimeout() (time.Duration, error) {
	leaseTimeout, err := time.ParseDuration(c.cm.Data[leaseTimeoutKey])
	if err != nil {
		return 0, err
	}
	return leaseTimeout, nil
}

func (c *configMap) ActiveLeases() (map[string]activeLease, error) {
	leases := map[string]activeLease{}
	aActiveLeasesValue := c.cm.Data[activeLeasesKey]
	if aActiveLeasesValue == "" {
		return leases, nil
	}
	if err := json.Unmarshal([]byte(aActiveLeasesValue), &leases); err != nil {
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
			logrus.Infof("Lease %s: %v != %v", key1, val1, val2)
			return true
		}
	}
	return false
}

// Update replaces active leases in the configmap with the provided active leases
// it only makes an update call if the active leases have changed
func (c *configMap) UpdateLeases(newActiveLeases map[string]activeLease) error {
	currentActiveLeases, err := c.ActiveLeases()
	if err != nil {
		panic(err)
	}
	if !isConfigMapUpdateRequired(newActiveLeases, currentActiveLeases) {
		return nil
	}

	leases := map[string]string{}
	for clientId, lease := range newActiveLeases {
		leases[clientId] = fmt.Sprintf("%d", lease.PermitId)
	}
	logrus.Infof("Updating configmap: %v", leases)

	activeLeasesValue, err := json.Marshal(newActiveLeases)
	if err != nil {
		return err
	}
	c.cm.Data[activeLeasesKey] = string(activeLeasesValue)

	c.cm, err = core.Instance().UpdateConfigMap(c.cm)
	if err != nil {
		return err
	}

	return nil
}

func (c *configMap) Refresh() error {
	cm, err := core.Instance().GetConfigMap(c.Name(), c.Namespace())
	if err != nil {
		return err
	}
	c.cm = cm
	return nil
}
