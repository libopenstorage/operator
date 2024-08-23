package resource_gateway

import "time"

const (
	// ResourceGatewayStr is the common string for resource-gateway components
	resourceGatewayStr = "resource-gateway"
	// defaultNamespace is the default namespace to create semaphore configmap
	defaultNamespace = "kube-system"
	// defaultConfigMapName is the default name for semaphore configmap
	defaultConfigMapName = resourceGatewayStr
	// defaultConfigMapLabels are the default labels applied to semaphore configmap
	defaultConfigMapLabels = "name=resource-gateway"
	// defaultConfigMapUpdatePeriod is the default time period between configmap updates
	defaultConfigMapUpdatePeriod = 1 * time.Second
	// defaultDeadClientTimeout is the default time period after which a node is considered dead
	defaultDeadClientTimeout = 10 * time.Second
	// defaultMaxQueueSize is the default max queue size for semaphore server
	defaultMaxQueueSize = 5000
)

type SemaphoreConfig struct {
	NPermits              uint32
	ConfigMapName         string
	ConfigMapNamespace    string
	ConfigMapLabels       map[string]string
	ConfigMapUpdatePeriod time.Duration
	LeaseTimeout          time.Duration
	DeadClientTimeout     time.Duration
	MaxQueueSize          uint
}

func NewSemaphoreConfig() *SemaphoreConfig {
	return &SemaphoreConfig{
		ConfigMapNamespace:    defaultNamespace,
		ConfigMapName:         defaultConfigMapName,
		ConfigMapLabels:       map[string]string{"name": resourceGatewayStr},
		ConfigMapUpdatePeriod: defaultConfigMapUpdatePeriod,
		DeadClientTimeout:     defaultDeadClientTimeout,
		MaxQueueSize:          defaultMaxQueueSize,
	}
}

func copySemaphoreConfig(semaphoreConfig *SemaphoreConfig) *SemaphoreConfig {
	configMapLabels := make(map[string]string)
	for k, v := range semaphoreConfig.ConfigMapLabels {
		configMapLabels[k] = v
	}
	return &SemaphoreConfig{
		NPermits:              semaphoreConfig.NPermits,
		ConfigMapName:         semaphoreConfig.ConfigMapName,
		ConfigMapNamespace:    semaphoreConfig.ConfigMapNamespace,
		ConfigMapLabels:       configMapLabels,
		ConfigMapUpdatePeriod: semaphoreConfig.ConfigMapUpdatePeriod,
		DeadClientTimeout:     semaphoreConfig.DeadClientTimeout,
		LeaseTimeout:          semaphoreConfig.LeaseTimeout,
		MaxQueueSize:          semaphoreConfig.MaxQueueSize,
	}
}
