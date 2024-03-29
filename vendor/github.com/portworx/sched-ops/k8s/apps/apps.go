package apps

import (
	"fmt"
	"os"
	"sync"

	"github.com/portworx/sched-ops/k8s/common"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appsv1client "k8s.io/client-go/kubernetes/typed/apps/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	storagev1client "k8s.io/client-go/kubernetes/typed/storage/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	instance Ops
	once     sync.Once

	deleteForegroundPolicy = metav1.DeletePropagationForeground
)

// Ops is an interface to perform kubernetes related operations on the apps resources.
type Ops interface {
	DaemonSetOps
	DeploymentOps
	StatefulSetOps
	ReplicaSetOps

	// SetConfig sets the config and resets the client
	SetConfig(config *rest.Config)
}

// Instance returns a singleton instance of the client.
func Instance() Ops {
	once.Do(func() {
		if instance == nil {
			instance = &Client{}
		}
	})
	return instance
}

// SetInstance replaces the instance with the provided one. Should be used only for testing purposes.
func SetInstance(i Ops) {
	instance = i
}

// New builds a new apps client.
func New(apps appsv1client.AppsV1Interface, core corev1client.CoreV1Interface) *Client {
	return &Client{
		apps: apps,
		core: core,
	}
}

// NewForConfig builds a new apps client for the given config.
func NewForConfig(c *rest.Config) (*Client, error) {
	apps, err := appsv1client.NewForConfig(c)
	if err != nil {
		return nil, err
	}

	core, err := corev1client.NewForConfig(c)
	if err != nil {
		return nil, err
	}

	return &Client{
		apps: apps,
		core: core,
	}, nil
}

// NewInstanceFromConfigFile returns new instance of client by using given
// config file
func NewInstanceFromConfigFile(config string) (Ops, error) {
	newInstance := &Client{}
	err := newInstance.loadClientFromKubeconfig(config)
	if err != nil {
		return nil, err
	}
	return newInstance, nil
}

// Client provides a wrapper for the kubernetes apps client.
type Client struct {
	config  *rest.Config
	apps    appsv1client.AppsV1Interface
	core    corev1client.CoreV1Interface
	storage storagev1client.StorageV1Interface
}

// SetConfig sets the config and resets the client
func (c *Client) SetConfig(cfg *rest.Config) {
	c.config = cfg
	c.apps = nil
	c.core = nil
}

// initClient the k8s client if uninitialized
func (c *Client) initClient() error {
	if c.apps != nil && c.core != nil {
		return nil
	}

	return c.setClient()
}

// setClient instantiates a client.
func (c *Client) setClient() error {
	var err error

	if c.config != nil {
		err = c.loadClient()
	} else {
		kubeconfig := os.Getenv("KUBECONFIG")
		if len(kubeconfig) > 0 {
			err = c.loadClientFromKubeconfig(kubeconfig)
		} else {
			err = c.loadClientFromServiceAccount()
		}

	}

	return err
}

// loadClientFromServiceAccount loads a k8s client from a ServiceAccount specified in the pod running px
func (c *Client) loadClientFromServiceAccount() error {
	config, err := rest.InClusterConfig()
	if err != nil {
		return err
	}

	c.config = config
	return c.loadClient()
}

func (c *Client) loadClientFromKubeconfig(kubeconfig string) error {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}

	c.config = config
	return c.loadClient()
}

func (c *Client) loadClient() error {
	if c.config == nil {
		return fmt.Errorf("rest config is not provided")
	}

	var err error
	err = common.SetRateLimiter(c.config)
	if err != nil {
		return err
	}
	c.apps, err = appsv1client.NewForConfig(c.config)
	if err != nil {
		return err
	}

	c.core, err = corev1client.NewForConfig(c.config)
	if err != nil {
		return err
	}

	return nil
}
