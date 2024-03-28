package main

import (
	"bytes"
	"fmt"
	"io"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"os"
	"path/filepath"
	"sigs.k8s.io/yaml"
	"strconv"
	"strings"
)

// convertToYaml converts a k8s object to YAML string
func (k8s *k8sRetriever) convertToYaml(object interface{}) (string, error) {
	bytesVar, err := yaml.Marshal(object)
	if err != nil {
		return "", err
	}
	return string(bytesVar), nil
}

// writeToFile writes the kubernetes resource to a file in the output directory, sets yaml as the default extension
func (k8s *k8sRetriever) writeToFile(resourceName string, resourceType string, dataToProcess string, basePath string, fileExtensions ...string) error {
	extension := "yaml" // default extension
	if len(fileExtensions) > 0 {
		extension = fileExtensions[0] // if an extension was provided, use it
	}

	fileName := fmt.Sprintf("%s/%s_%s.%s", basePath, resourceType, resourceName, extension)
	file, err := os.Create(fileName) // This creates or truncates the file
	if err != nil {
		return err
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			k8s.loggerToUse.Errorf("Error closing file: %s", err.Error())
		}
	}(file)

	if _, err := file.WriteString(dataToProcess); err != nil {
		return err
	}

	return nil
}

// createDynamicClient returns a dynamic client based on the environment and its configuration
func (k8s *k8sRetriever) createDynamicClient() (dynamic.Interface, error) {

	dc, err := dynamic.NewForConfig(k8s.restConfig)
	if err != nil {
		return nil, err
	}
	return dc, nil
}

// isOpenShift returns true if the cluster is an OpenShift cluster
func (k8s *k8sRetriever) isOpenShift(dc dynamic.Interface) bool {
	sccGVR := schema.GroupVersionResource{
		Group:    "security.openshift.io",
		Version:  "v1",
		Resource: "securitycontextconstraints",
	}

	sccList, err := dc.Resource(sccGVR).List(k8s.context, meta.ListOptions{})
	if err != nil {
		return false
	}
	return len(sccList.Items) > 0
}

// patchOpenShiftSCC, assumes dynamic client (dc) is already initialized and correctly configured
func (k8s *k8sRetriever) patchOpenShiftSCC(dc dynamic.Interface, namespace string, sccToPatch string) error {
	if sccToPatch == "" {
		sccToPatch = "anyuid"
	}

	sccGVR := schema.GroupVersionResource{
		Group:    "security.openshift.io",
		Version:  "v1",
		Resource: "securitycontextconstraints",
	}

	// create the user string
	userToAdd := "system:serviceaccount:" + namespace + ":k8s-retriever-sa"

	// get the current SCC to check the users
	sccUnstructured, err := dc.Resource(sccGVR).Get(k8s.context, "privileged", meta.GetOptions{})
	if err != nil {
		return err
	}

	// check if the user already exists in the SCC
	users, found, err := unstructured.NestedStringSlice(sccUnstructured.UnstructuredContent(), "users")
	if err != nil {
		return err
	}

	if found {
		for _, user := range users {
			if user == userToAdd {
				k8s.loggerToUse.Printf("User %s already exists in intended SCC, %s", userToAdd, sccToPatch)
				return nil
			}
		}
	}

	// patch the SCC
	patchData := []byte(`[
		{
			"path": "/users/-",
			"op": "add",
			"value": "` + userToAdd + `"
		}
	]`)

	_, err = dc.Resource(sccGVR).Patch(k8s.context, sccToPatch, types.JSONPatchType, patchData, meta.PatchOptions{})
	if err != nil {
		k8s.loggerToUse.Errorf("Error patching SCC: %s", err.Error())
		return err
	}

	return nil
}

// removeOpenShiftSCC, assumes dynamic client (dc) is already initialized and correctly configured
func (k8s *k8sRetriever) removeOpenShiftSCC(dc dynamic.Interface, namespace string, sccToPatch string) error {
	if sccToPatch == "" {
		sccToPatch = "anyuid"
	}

	sccGVR := schema.GroupVersionResource{
		Group:    "security.openshift.io",
		Version:  "v1",
		Resource: "securitycontextconstraints",
	}

	// create the user string
	userToRemove := "system:serviceaccount:" + namespace + ":k8s-retriever-sa"

	// get the current SCC to check the users
	sccUnstructured, err := dc.Resource(sccGVR).Get(k8s.context, sccToPatch, meta.GetOptions{})
	if err != nil {
		return err
	}

	// check if the user already exists in the SCC
	users, found, err := unstructured.NestedStringSlice(sccUnstructured.UnstructuredContent(), "users")
	if err != nil {
		return err
	}

	if found {
		for i, user := range users {
			if user == userToRemove {
				// remove the user from the SCC
				patchData := []byte(`[
					{
						"path": "/users/` + strconv.Itoa(i) + `",
						"op": "remove"
					}
				]`)

				_, err = dc.Resource(sccGVR).Patch(k8s.context, sccToPatch, types.JSONPatchType, patchData, meta.PatchOptions{})
				if err != nil {
					k8s.loggerToUse.Errorf("Error removing user from SCC: %s", err.Error())
					return err
				}

				return nil
			}
		}
	}

	k8s.loggerToUse.Printf("User %s does not exist in intended SCC, %s", userToRemove, sccToPatch)
	return nil
}

// listDeployments lists all deployments in a namespace and writes them to a file in the output directory
func (k8s *k8sRetriever) listDeployments(namespace string) error {

	saveFilesPath := k8s.outputDir + "/px-deployments"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	deployments, err := k8s.k8sClient.AppsV1().Deployments(namespace).List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, deployment := range deployments.Items {
		deployYaml, err := k8s.convertToYaml(deployment)
		if err != nil {
			return err
		}
		k8s.loggerToUse.Infof("Deployment: %s\n", deployment.Name)

		if err := k8s.writeToFile(deployment.Name, "deployment", deployYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing pod YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listPods lists all pods in a namespace and writes them to a file in the output directory
func (k8s *k8sRetriever) listPods(namespace string, labelSelector string) error {

	saveFilesPath := k8s.outputDir + "/px-pods"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	options := meta.ListOptions{
		LabelSelector: labelSelector,
	}

	if labelSelector == "" {
		options = meta.ListOptions{}
	}

	pods, err := k8s.k8sClient.CoreV1().Pods(namespace).List(k8s.context, options)
	if err != nil {
		return err
	}

	for _, pod := range pods.Items {
		podYaml, err := k8s.convertToYaml(pod)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting pod to YAML: %s", err.Error())
			continue
		}

		events, err := k8s.k8sClient.CoreV1().Events(namespace).List(k8s.context, meta.ListOptions{
			FieldSelector: fmt.Sprintf("involvedObject.kind=Pod,involvedObject.name=%s", pod.Name),
		})

		if err != nil {
			k8s.loggerToUse.Errorf("Error fetching pod events: %s", err.Error())
			continue
		}

		eventsYaml, err := k8s.convertToYaml(events.Items)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting events to YAML: %s", err.Error())
			continue
		}

		// append events to pod YAML
		podYaml = podYaml + "\n\nEvents:\n" + eventsYaml

		k8s.loggerToUse.Infof("Pod: %s, Node: %s", pod.Name, pod.Spec.NodeName)
		podFileName := fmt.Sprintf("%s-%s", pod.Spec.NodeName, pod.Name)
		if err := k8s.writeToFile(podFileName, "pod", podYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing pod YAML to file: %s", err.Error())
		}

		// Fetch logs for each container within the pod
		for _, container := range pod.Spec.Containers {

			podLogOptions := &core.PodLogOptions{
				Container: container.Name,
			}
			req := k8s.k8sClient.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, podLogOptions)
			podLogs, err := req.Stream(k8s.context)
			if err != nil {
				k8s.loggerToUse.Errorf("Error in opening stream: %s", err.Error())
				continue
			}

			buf := new(bytes.Buffer)
			_, err = io.Copy(buf, podLogs)
			if err != nil {
				k8s.loggerToUse.Errorf("Error in copy information from podLogs to buf: %s", err.Error())
				continue
			}

			err = podLogs.Close()
			if err != nil {
				k8s.loggerToUse.Errorf("Error in closing stream: %s", err.Error())
				continue
			}

			str := buf.String()

			// Write logs to a separate file with .log extension
			logFileName := fmt.Sprintf("%s-%s-%s", pod.Spec.NodeName, pod.Name, container.Name)
			if err := k8s.writeToFile(logFileName, "logs", str, saveFilesPath, "log"); err != nil {
				k8s.loggerToUse.Errorf("Error writing pod logs to file: %s", err.Error())
			}
		}
	}

	return nil
}

// listServices lists all services in a namespace and writes them to a file in the output directory
func (k8s *k8sRetriever) listServices(namespace string) error {

	saveFilesPath := k8s.outputDir + "/px-services"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	services, err := k8s.k8sClient.CoreV1().Services(namespace).List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, service := range services.Items {
		serviceYaml, err := k8s.convertToYaml(service)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting service to YAML: %s", err.Error())
			continue
		}
		k8s.loggerToUse.Infof("Service: %s", service.Name)
		if err := k8s.writeToFile(service.Name, "service", serviceYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing service YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listSharedv4Services lists all sharedv4 services in a namespace and writes them to a file in the output directory
func (k8s *k8sRetriever) listSharedv4Services(namespace string) error {
	saveFilesPath := k8s.outputDir + "/px-sharedv4-services"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	labelSelector := "portworx.io/volid"

	services, err := k8s.k8sClient.CoreV1().Services(namespace).List(k8s.context, meta.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return err
	}

	for _, service := range services.Items {
		serviceYaml, err := k8s.convertToYaml(service)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting service to YAML: %s", err.Error())
			continue
		}
		k8s.loggerToUse.Infof("Sharedv4 Service: %s", service.Name)
		if err := k8s.writeToFile(service.Name, "service", serviceYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing service YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listStorageClusters lists all storage clusters in a namespace and writes them to a file in the output directory
func (k8s *k8sRetriever) listStorageClusters(namespace string) error {

	saveFilesPath := k8s.outputDir + "/px-storageCluster"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	dynamicClient, err := dynamic.NewForConfig(k8s.restConfig)
	if err != nil {
		return err
	}

	gvr := schema.GroupVersionResource{Group: "core.libopenstorage.org", Version: "v1", Resource: "storageclusters"}

	k8s.loggerToUse.Infof("Fetching Portworx Storage Clusters in namespace: %s", namespace)

	storageClusters, err := dynamicClient.Resource(gvr).Namespace(namespace).List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, storageCluster := range storageClusters.Items {
		storageClusterYaml, err := k8s.convertToYaml(storageCluster)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting storagecluster to YAML: %s", err.Error())
			continue
		}
		k8s.loggerToUse.Infof("Portworx Storage Clusters: %s\n", storageCluster.GetName())

		if err := k8s.writeToFile(storageCluster.GetName(), "storagecluster", storageClusterYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing storagecluster YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listStorageNodes lists all storage nodes in a namespace and writes them to a file in the output directory
func (k8s *k8sRetriever) listStorageNodes(namespace string) error {

	saveFilesPath := k8s.outputDir + "/px-storageNodes"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	dynamicClient, err := dynamic.NewForConfig(k8s.restConfig)
	if err != nil {
		return err
	}

	gvr := schema.GroupVersionResource{Group: "core.libopenstorage.org", Version: "v1", Resource: "storagenodes"}

	k8s.loggerToUse.Infof("Fetching Portworx Storage Nodes in namespace: %s", namespace)

	storageNodes, err := dynamicClient.Resource(gvr).Namespace(namespace).List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, storageNode := range storageNodes.Items {
		storageNodeYaml, err := k8s.convertToYaml(storageNode)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting storagenode to YAML: %s", err.Error())
			continue
		}
		k8s.loggerToUse.Infof("StorageNode: %s", storageNode.GetName())
		if err := k8s.writeToFile(storageNode.GetName(), "storagenode", storageNodeYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing storagenode YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listDaemonSets lists all daemonsets in a namespace and writes them to a file in the output directory
func (k8s *k8sRetriever) listDaemonSets(namespace string) error {

	saveFilesPath := k8s.outputDir + "/px-daemonsets"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	daemonSets, err := k8s.k8sClient.AppsV1().DaemonSets(namespace).List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, ds := range daemonSets.Items {
		dsYaml, err := k8s.convertToYaml(ds)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting daemonset to YAML: %s", err.Error())
			continue
		}

		k8s.loggerToUse.Infof("DaemonSet: %s", ds.Name)

		if err := k8s.writeToFile(ds.Name, "daemonset", dsYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing daemonset YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listStatefulSets lists all statefulsets in a namespace and writes them to a file in the output directory
func (k8s *k8sRetriever) listStatefulSets(namespace string) error {

	saveFilesPath := k8s.outputDir + "/px-statefulsets"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	statefulSets, err := k8s.k8sClient.AppsV1().StatefulSets(namespace).List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, sts := range statefulSets.Items {
		stsYaml, err := k8s.convertToYaml(sts)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting statefulset to YAML: %s", err.Error())
			continue
		}

		k8s.loggerToUse.Infof("StatefulSet: %s", sts.Name)

		if err := k8s.writeToFile(sts.Name, "statefulset", stsYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing statefulset YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listPVCs lists all PVCs in a namespace and writes them to a file in the output directory
func (k8s *k8sRetriever) listPVCs(namespace string) error {

	saveFilesPath := k8s.outputDir + "/cluster-PVCs"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	pvcs, err := k8s.k8sClient.CoreV1().PersistentVolumeClaims(namespace).List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	pvcprefix := "pvc-" + namespace

	for _, pvc := range pvcs.Items {
		pvcYaml, err := k8s.convertToYaml(pvc)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting PVC to YAML: %s", err.Error())
			continue
		}
		k8s.loggerToUse.Infof("PVC %s found on namespace: %s", pvc.Name, namespace)

		events, err := k8s.k8sClient.CoreV1().Events(namespace).List(k8s.context, meta.ListOptions{
			FieldSelector: fmt.Sprintf("involvedObject.kind=PersistentVolumeClaim,involvedObject.name=%s", pvc.Name),
		})

		if err != nil {
			k8s.loggerToUse.Errorf("Error fetching pod events: %s", err.Error())
			continue
		}

		eventsYaml, err := k8s.convertToYaml(events.Items)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting events to YAML: %s", err.Error())
			continue
		}

		// append events to PVC YAML
		pvcYaml = pvcYaml + "\n\nEvents:\n" + eventsYaml

		if err := k8s.writeToFile(pvc.Name, pvcprefix, pvcYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing pvc YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listAllPVCs lists all PVCs in all namespaces and writes them to a file in the output directory
func (k8s *k8sRetriever) listAllPVCs() error {

	namespaces, err := k8s.k8sClient.CoreV1().Namespaces().List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, ns := range namespaces.Items {

		k8s.loggerToUse.Infof("Checking for PVCs on Namespace: %s", ns.Name)

		if err := k8s.listPVCs(ns.Name); err != nil {
			k8s.loggerToUse.Errorf("Error listing PVCs in namespace %s: %v", ns.Name, err)
		}
	}

	return nil
}

// listAllPVs lists all PVs in all namespaces and writes them to a file in the output directory
func (k8s *k8sRetriever) listAllPVs() error {

	saveFilesPath := k8s.outputDir + "/cluster-PVs"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	pvs, err := k8s.k8sClient.CoreV1().PersistentVolumes().List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, pv := range pvs.Items {
		pvYaml, err := k8s.convertToYaml(pv)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting pod to YAML: %s", err.Error())
			continue
		}

		k8s.loggerToUse.Infof("PV: %s", pv.Name)

		if err := k8s.writeToFile(pv.Name, "pv", pvYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing pv YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listKubernetesWorkerNodes lists all worker nodes in a Kubernetes cluster and writes them to a file in the output directory
func (k8s *k8sRetriever) listKubernetesWorkerNodes() error {

	saveFilesPath := k8s.outputDir + "/cluster-workerNodes"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	nodes, err := k8s.k8sClient.CoreV1().Nodes().List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, node := range nodes.Items {
		// Skip master nodes
		if _, hasMasterRole := node.Labels["node-role.kubernetes.io/master"]; hasMasterRole {
			continue
		}

		nodeYaml, err := k8s.convertToYaml(node)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting node to YAML: %s", err.Error())
			continue
		}

		events, err := k8s.k8sClient.CoreV1().Events("").List(k8s.context, meta.ListOptions{
			FieldSelector: fmt.Sprintf("involvedObject.kind=Node,involvedObject.name=%s", node.Name),
		})

		if err != nil {
			k8s.loggerToUse.Errorf("Error fetching node events: %s", err.Error())
			continue
		}

		eventsYaml, err := k8s.convertToYaml(events.Items)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting events to YAML: %s", err.Error())
			continue
		}

		// append events to node YAML
		nodeYaml = nodeYaml + "\n\nEvents:\n" + eventsYaml

		k8s.loggerToUse.Infof("Node: %s", node.Name)

		if err := k8s.writeToFile(node.Name, "node", nodeYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing node YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listStorageClasses lists all storage classes in a Kubernetes cluster and writes them to a file in the output directory
func (k8s *k8sRetriever) listStorageClasses() error {

	saveFilesPath := k8s.outputDir + "/cluster-storageClasses"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	storageClasses, err := k8s.k8sClient.StorageV1().StorageClasses().List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, storageClass := range storageClasses.Items {
		storageClassYaml, err := k8s.convertToYaml(storageClass)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting storage class to YAML: %s", err.Error())
			continue
		}

		k8s.loggerToUse.Infof("StorageClass: %s", storageClass.Name)

		if err := k8s.writeToFile(storageClass.Name, "storageclass", storageClassYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing storage class YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listVolumePlacementStrategies lists all volume placement strategies in a Kubernetes cluster and writes them to a file in the output directory
func (k8s *k8sRetriever) listVolumePlacementStrategies() error {

	saveFilesPath := k8s.outputDir + "/px-VPSs"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	dynamicClient, err := dynamic.NewForConfig(k8s.restConfig)
	if err != nil {
		return err
	}

	gvr := schema.GroupVersionResource{Group: "portworx.io", Version: "v1beta2", Resource: "volumeplacementstrategies"}

	volumePlacementStrategies, err := dynamicClient.Resource(gvr).List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, volumePlacementStrategy := range volumePlacementStrategies.Items {
		VPSYaml, err := k8s.convertToYaml(volumePlacementStrategy)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting VPS to YAML: %s", err.Error())
			continue
		}
		k8s.loggerToUse.Infof("VolumePlacementStrategy: %s", volumePlacementStrategy.GetName())
		if err := k8s.writeToFile(volumePlacementStrategy.GetName(), "volumeplacementstrategy", VPSYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing VolumePlacementStrategy YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listStorageProfiles lists all storage profiles in a Kubernetes cluster and writes them to a file in the output directory
func (k8s *k8sRetriever) listStorageProfiles() error {

	saveFilesPath := k8s.outputDir + "/cluster-storageProfiles"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	dynamicClient, err := dynamic.NewForConfig(k8s.restConfig)
	if err != nil {
		return err
	}

	gvr := schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "storageprofiles"}

	storageProfiles, err := dynamicClient.Resource(gvr).List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, storageProfile := range storageProfiles.Items {
		storageProfileYaml, err := k8s.convertToYaml(storageProfile)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting Storage Profile to YAML: %s", err.Error())
			continue
		}
		k8s.loggerToUse.Infof("StorageProfile: %s", storageProfile.GetName())
		if err := k8s.writeToFile(storageProfile.GetName(), "storageprofile", storageProfileYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing storagecluster YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listVolumeSnapshotClasses lists all volume snapshot classes in a Kubernetes cluster and writes them to a file in the output directory
func (k8s *k8sRetriever) listVolumeSnapshotClasses() error {

	saveFilesPath := k8s.outputDir + "/px-VolumeSnapshotClasses"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	dynamicClient, err := dynamic.NewForConfig(k8s.restConfig)
	if err != nil {
		return err
	}

	gvr := schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotclasses"}

	volumeSnapshotClasses, err := dynamicClient.Resource(gvr).List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, volumeSnapshotClass := range volumeSnapshotClasses.Items {
		volumeSnapshotClassYaml, err := k8s.convertToYaml(volumeSnapshotClass)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting Volume Snapshot Class to YAML: %s", err.Error())
			continue
		}
		k8s.loggerToUse.Infof("VolumeSnapshotClass: %s", volumeSnapshotClass.GetName())
		if err := k8s.writeToFile(volumeSnapshotClass.GetName(), "volumesnapshotclass", volumeSnapshotClassYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing VolumeSnapshotClass YAML to file: %s", err.Error())
		}
	}

	return nil
}

// listClusterPairs lists all cluster pairs in a namespace and writes them to a file in the output directory
func (k8s *k8sRetriever) listClusterPairs() error {

	saveFilesPath := k8s.outputDir + "/stork-clusterPairs"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	dynamicClient, err := dynamic.NewForConfig(k8s.restConfig)
	if err != nil {
		return err
	}

	gvr := schema.GroupVersionResource{Group: "stork.libopenstorage.org", Version: "v1alpha1", Resource: "clusterpairs"}

	namespaces, err := k8s.k8sClient.CoreV1().Namespaces().List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, namespace := range namespaces.Items {

		clusterPairs, err := dynamicClient.Resource(gvr).Namespace(namespace.GetName()).List(k8s.context, meta.ListOptions{})
		if err != nil {
			return err
		}

		for _, clusterPair := range clusterPairs.Items {
			clusterPairYaml, err := k8s.convertToYaml(clusterPair)
			if err != nil {
				k8s.loggerToUse.Errorf("Error converting ClusterPair to YAML: %s", err.Error())
				continue
			}
			k8s.loggerToUse.Infof("ClusterPair: %s", clusterPair.GetName())
			clusterPairFileName := fmt.Sprintf("%s-%s", clusterPair.GetName(), namespace.GetName())
			if err := k8s.writeToFile(clusterPairFileName, "clusterpair", clusterPairYaml, saveFilesPath); err != nil {
				k8s.loggerToUse.Errorf("Error writing ClusterPair YAML to file: %s", err.Error())
			}
		}

	}

	return nil

}

// listBackupLocations lists all backup locations in a namespace and writes them to a file in the output directory
func (k8s *k8sRetriever) listBackupLocations() error {

	saveFilesPath := k8s.outputDir + "/stork-backupLocations"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	dynamicClient, err := dynamic.NewForConfig(k8s.restConfig)
	if err != nil {
		return err
	}

	gvr := schema.GroupVersionResource{Group: "stork.libopenstorage.org", Version: "v1alpha1", Resource: "backuplocations"}

	namespaces, err := k8s.k8sClient.CoreV1().Namespaces().List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, namespace := range namespaces.Items {

		backupLocations, err := dynamicClient.Resource(gvr).Namespace(namespace.GetName()).List(k8s.context, meta.ListOptions{})
		if err != nil {
			return err
		}

		for _, backupLocation := range backupLocations.Items {
			backupLocationYaml, err := k8s.convertToYaml(backupLocation)
			if err != nil {
				k8s.loggerToUse.Errorf("Error converting BackupLocation to YAML: %s", err.Error())
				continue
			}
			k8s.loggerToUse.Infof("BackupLocation: %s", backupLocation.GetName())
			backupLocationFileName := fmt.Sprintf("%s-%s", backupLocation.GetName(), namespace.GetName())
			if err := k8s.writeToFile(backupLocationFileName, "backuplocation", backupLocationYaml, saveFilesPath); err != nil {
				k8s.loggerToUse.Errorf("Error writing BackupLocation YAML to file: %s", err.Error())
			}
		}

	}

	return nil

}

// listJobs lists all jobs in a namespace and writes them to a file in the output directory
func (k8s *k8sRetriever) listJobs(namespace string) error {

	saveFilesPath := k8s.outputDir + "/px-jobs"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	jobs, err := k8s.k8sClient.BatchV1().Jobs(namespace).List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, job := range jobs.Items {
		jobYaml, err := k8s.convertToYaml(job)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting job to YAML: %s", err.Error())
			continue
		}
		k8s.loggerToUse.Infof("Job: %s", job.Name)
		if err := k8s.writeToFile(job.Name, "job", jobYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing job YAML to file: %s", err.Error())
		}
	}

	return nil
}

// namespaceExists checks if a namespace exists in the Kubernetes cluster
func (k8s *k8sRetriever) namespaceExists(namespace string) bool {

	_, err := k8s.k8sClient.CoreV1().Namespaces().Get(k8s.context, namespace, meta.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false
		}
		return false
	}
	return true
}

// listConfigMaps lists all configmaps in a namespace and writes them to a file in the output directory
func (k8s *k8sRetriever) listConfigMaps(namespace string) error {

	saveFilesPath := k8s.outputDir + "/px-configMaps"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	k8s.loggerToUse.Infof("Fetching ConfigMaps in namespace: %s", namespace)

	configMaps, err := k8s.k8sClient.CoreV1().ConfigMaps(namespace).List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, configMap := range configMaps.Items {
		configMapYaml, err := k8s.convertToYaml(configMap)
		if err != nil {
			k8s.loggerToUse.Errorf("Error converting ConfigMap to YAML: %s", err.Error())
			continue
		}
		k8s.loggerToUse.Infof("ConfigMap: %s", configMap.GetName())
		if err := k8s.writeToFile(configMap.GetName(), "configmap", configMapYaml, saveFilesPath); err != nil {
			k8s.loggerToUse.Errorf("Error writing ConfigMap YAML to file: %s", err.Error())
		}
	}

	return nil
}

// getPXConfigMapsKubeSystem gets a configmap from a partial name from kube-system namespace
func (k8s *k8sRetriever) getPXConfigMapsKubeSystem(partialName string) error {

	saveFilesPath := k8s.outputDir + "/cluster-configMaps"
	err := os.MkdirAll(saveFilesPath, 0750)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		return err
	}

	configMaps, err := k8s.k8sClient.CoreV1().ConfigMaps("kube-system").List(k8s.context, meta.ListOptions{})
	if err != nil {
		return err
	}

	for _, configMap := range configMaps.Items {
		if strings.Contains(configMap.Name, partialName) {
			configMapYaml, err := k8s.convertToYaml(configMap)
			if err != nil {
				k8s.loggerToUse.Errorf("Error converting ConfigMap to YAML: %s", err.Error())
				continue
			}

			k8s.loggerToUse.Infof("ConfigMap: %s", configMap.GetName())

			configMapFileName := fmt.Sprintf("%s-%s", configMap.GetName(), "kube-system")
			if err := k8s.writeToFile(configMapFileName, "configmap", configMapYaml, saveFilesPath); err != nil {
				k8s.loggerToUse.Errorf("Error writing ConfigMap YAML to file: %s", err.Error())
			}

		}
	}

	return nil
}

// deleteEmptyDirs deletes empty directories in the given root directory
func (k8s *k8sRetriever) deleteEmptyDirs(root string) error {

	var dirs []string

	// First, collect all directories in a slice.
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Process directories in reverse order (bottom-up).
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		files, err := os.ReadDir(dir)
		if err != nil {
			return err
		}

		// If the directory is empty, remove it.
		if len(files) == 0 {
			if err := os.Remove(dir); err != nil {
				return err
			}
		}
	}

	return nil
}
