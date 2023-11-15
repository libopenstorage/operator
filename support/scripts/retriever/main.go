package main

import (
	"fmt"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/runtime/schema"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"log"
	"os"
	"runtime/debug"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var PxNamespace = os.Getenv("PX_NAMESPACE")
var outputPath = os.Getenv("RETRIEVER_OUTPUT_PATH")
var kubeconfigPath = os.Getenv("KUBECONFIG")
var hostname = os.Getenv("HOSTNAME")

// main function to run the retriever
func main() {

	defer func() {
		if r := recover(); r != nil {
			fmt.Println("Recovered from panic:", r)
			debug.PrintStack()
		}
	}()

	logger := setupLogging()
	if logger == nil {
		log.Fatalf("Failed to set up logger")
		return
	}

	k8sRetriever, err := initializeRetriever(logger, nil, afero.NewOsFs())
	if err != nil {
		log.Print(err)
		return
	}

	if hostname == "" {
		hostname, err = os.Hostname()
		if err != nil {
			hostname = "localhost"
		}
	}

	k8sRetriever.loggerToUse.Info("---Starting Kubernetes client!---")
	k8sRetriever.loggerToUse.Infof("Job is running on Kubernetes Node: %s", hostname)
	k8sRetriever.loggerToUse.Infof("Creating YAML files for all resources in namespace: %s", PxNamespace)
	k8sRetriever.loggerToUse.Infof("YAML files will be created in: %s", outputPath)

	// Create a slice of k8sObjectParams to iterate over and fetch each resource type
	resourcesToFetch := []k8sObjectParams{
		{
			GVK: schema.GroupVersionKind{
				Group:   "core.libopenstorage.org",
				Kind:    "StorageClusterList",
				Version: "v1",
			},
			ListOpts:     []client.ListOption{client.InNamespace(PxNamespace)},
			ResourceName: "storagecluster",
			Namespace:    PxNamespace,
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "core.libopenstorage.org",
				Kind:    "StorageNodeList",
				Version: "v1",
			},
			ListOpts:     []client.ListOption{client.InNamespace(PxNamespace)},
			ResourceName: "storagenode",
			Namespace:    PxNamespace,
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "apps",
				Kind:    "DeploymentList",
				Version: "v1",
			},
			ListOpts:     []client.ListOption{client.InNamespace(PxNamespace)},
			ResourceName: "deployment",
			Namespace:    PxNamespace,
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "apps",
				Kind:    "DaemonSetList",
				Version: "v1",
			},
			ListOpts:     []client.ListOption{client.InNamespace(PxNamespace)},
			ResourceName: "daemonset",
			Namespace:    PxNamespace,
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "apps",
				Kind:    "StatefulSetList",
				Version: "v1",
			},
			ListOpts:     []client.ListOption{client.InNamespace(PxNamespace)},
			ResourceName: "statefulset",
			Namespace:    PxNamespace,
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "",
				Kind:    "PersistentVolumeList",
				Version: "v1",
			},
			ListOpts:     []client.ListOption{},
			ResourceName: "pv",
			Namespace:    "",
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "",
				Kind:    "NodeList",
				Version: "v1",
			},
			ListOpts:     []client.ListOption{},
			ResourceName: "node",
			Namespace:    "",
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "",
				Kind:    "PersistentVolumeClaimList",
				Version: "v1",
			},
			ListOpts:     []client.ListOption{},
			ResourceName: "pvc",
			Namespace:    "",
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "cdi.kubevirt.io",
				Kind:    "StorageProfileList",
				Version: "v1beta1",
			},
			ListOpts:     []client.ListOption{},
			ResourceName: "storageprofile",
			Namespace:    PxNamespace,
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "portworx.io",
				Kind:    "VolumePlacementStrategyList",
				Version: "v1beta2",
			},
			ListOpts:     []client.ListOption{},
			ResourceName: "vps",
			Namespace:    "",
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "storage.k8s.io",
				Kind:    "StorageClassList",
				Version: "v1",
			},
			ListOpts:     []client.ListOption{},
			ResourceName: "storageclass",
			Namespace:    "",
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "snapshot.storage.k8s.io",
				Kind:    "VolumeSnapshotClassList",
				Version: "v1",
			},
			ListOpts:     []client.ListOption{},
			ResourceName: "volumesnapshotclass",
			Namespace:    "",
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "stork.libopenstorage.org",
				Kind:    "ClusterPairList",
				Version: "v1alpha1",
			},
			ListOpts:     []client.ListOption{},
			ResourceName: "clusterpair",
			Namespace:    "",
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "stork.libopenstorage.org",
				Kind:    "BackupLocationList",
				Version: "v1alpha1",
			},
			ListOpts:     []client.ListOption{},
			ResourceName: "backuplocation",
			Namespace:    "",
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "",
				Kind:    "EventList",
				Version: "v1",
			},
			ListOpts:     []client.ListOption{},
			ResourceName: "k8s-event",
			Namespace:    "",
		},
		{
			GVK: schema.GroupVersionKind{
				Group:   "",
				Kind:    "ConfigMapList",
				Version: "v1",
			},
			ListOpts:     []client.ListOption{client.InNamespace("kube-system")},
			ResourceName: "configmap",
			Namespace:    "kube-system",
		},
	}

	// Iterate over the resources and fetch each one
	for _, resource := range resourcesToFetch {
		err := k8sRetriever.listKubernetesObjects(resource.Namespace, resource.GVK, resource.ListOpts, resource.ResourceName)
		if err != nil {
			k8sRetriever.loggerToUse.Errorf("Failed to list and handle %ss: %v", resource.ResourceName, err)
		}
	}

	for _, pod := range listOfPods {
		k8sRetriever.loggerToUse.Printf("Pod: %s", pod.Name)
		if err := k8sRetriever.listPods(PxNamespace, pod.Label); err != nil {
			k8sRetriever.loggerToUse.Errorf("error listing pods: %v", err)
		}
	}

	for _, pod := range listOfPods {
		k8sRetriever.loggerToUse.Printf("Logs for Pod: %s", pod.Name)
		if err := k8sRetriever.getLogsForPod(PxNamespace, pod.Label); err != nil {
			k8sRetriever.loggerToUse.Errorf("error listing pods: %v", err)
		}
	}

	err = k8sRetriever.retrieveMultipathConf(hostname)
	if err != nil {
		k8sRetriever.loggerToUse.Errorf("Failed to retrieve multipath.conf: %v", err)
	}

	k8sRetriever.loggerToUse.Info("---Finished Kubernetes client!---")

}
