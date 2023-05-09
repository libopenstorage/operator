package k8s

import (
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"k8s.io/api/core/v1"
)

type PluginConfigMap struct {
	K8sClient client.Client
}

func NewPluginConfigMap(client client.Client) *PluginConfigMap {
	return &PluginConfigMap{K8sClient: client}
}

func (c *PluginConfigMap) CreateConfigMap() {
	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "px-plugin-configmap",
			Namespace:       "openshift-operators",
			,
		},
		Data:       map[string]string{
			"key1" : "val1",
		},

	}
	created , err := CreateOrUpdateConfigMap(c.K8sClient, cm, nil)
	if err != nil || !created {
		fmt.Println("configmap is not created , err : ", err)
		return
	}
	fmt.Println("Configmap for plugin created")
}


