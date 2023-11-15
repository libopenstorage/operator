package main

import (
	"errors"
	"fmt"
	"github.com/libopenstorage/operator/pkg/client/clientset/versioned/scheme"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// Use this method in your actual retriever logic
func (k8s *k8sRetriever) processContainerForLogs(pod *unstructured.Unstructured, containerItem interface{}, path string) {
	err := k8s.containerProcessor.ProcessContainer(pod, containerItem, path)
	if err != nil {
		return
	}
}

// ConvertToYaml converts a k8s object to YAML string
func (k8s *k8sRetriever) ConvertToYaml(object interface{}) (string, error) {
	bytesVar, err := yaml.Marshal(object)
	if err != nil {
		return "", err
	}
	return string(bytesVar), nil
}

// writeToFile writes the kubernetes resource to a file in the output directory, sets yaml as the default extension
func (r *RealWriteToFile) writeToFile(resourceName string, resourceType string, dataToProcess string, basePath string, fileExtensions ...string) error {
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
			return
		}
	}(file)

	if _, err := file.WriteString(dataToProcess); err != nil {
		return err
	}

	return nil
}

// constructPath constructs the path to save the files to
func (k8s *k8sRetriever) constructPath(subDir string) string {
	return fmt.Sprintf("%s/%s", k8s.outputPath, subDir)
}

// processK8sObjects processes a list of k8s objects using the provided processor function
func (k8s *k8sRetriever) processK8sObjects(objList *unstructured.UnstructuredList, processor objectProcessorFunc) error {
	for _, obj := range objList.Items {
		if err := processor(&obj); err != nil {
			k8s.loggerToUse.Errorf("Processing object %s resulted in error: %s", obj.GetName(), err.Error())
			return err
		}
	}
	return nil
}

// ensureDirectory creates the directory if it doesn't exist
func (k8s *k8sRetriever) ensureDirectory(path string) error {
	err := k8s.fs.MkdirAll(path, 0766)
	if err != nil {
		k8s.loggerToUse.Errorf("error creating directory: %s", err.Error())
	}
	return err
}

// fetchK8sObjects fetches k8s objects using the provided options
func (k8s *k8sRetriever) fetchK8sObjects(gvk schema.GroupVersionKind, namespace string, opts []client.ListOption) (*unstructured.UnstructuredList, error) {
	objList := &unstructured.UnstructuredList{}
	objList.SetGroupVersionKind(gvk)

	listOpts := &client.ListOptions{Namespace: namespace}
	for _, opt := range opts {
		opt.ApplyToList(listOpts)
	}

	err := k8s.k8sClient.List(k8s.context, objList, listOpts)
	if err != nil {
		return nil, err
	}
	return objList, nil
}

// listPods fetches all pods in a namespace and writes them to files
func (k8s *k8sRetriever) listPods(namespace string, labelSelector string) error {
	saveFilesPath := k8s.constructPath("px-pods")
	if err := k8s.ensureDirectory(saveFilesPath); err != nil {
		return err
	}

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return fmt.Errorf("error converting label selector to labels map: %v", err)
	}

	opts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels(labelMap),
	}

	podList, err := k8s.fetchK8sObjects(schema.GroupVersionKind{
		Group:   "",
		Kind:    "PodList",
		Version: "v1",
	}, namespace, opts)
	if err != nil {
		return fmt.Errorf("error retrieving pods: %s", err.Error())
	}

	return k8s.processK8sObjects(podList, func(obj *unstructured.Unstructured) error {
		return k8s.processPodAndEvents(obj, saveFilesPath)
	})
}

// internalProcessPodAndEvents processes a single pod and its events
func (k8s *k8sRetriever) processPodAndEvents(pod *unstructured.Unstructured, saveFilesPath string) error {
	podYaml, err := k8s.yamlConverter.ConvertToYaml(pod)
	if err != nil {
		return err
	}

	eventList := &unstructured.UnstructuredList{}
	eventList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "",
		Kind:    "EventList",
		Version: "v1",
	})

	eventSelector := client.MatchingFields{"involvedObject.kind": "Pod", "involvedObject.name": pod.GetName()}
	err = k8s.k8sClient.List(k8s.context, eventList, eventSelector)
	if err != nil {
		k8s.loggerToUse.Errorf("error fetching pod events: %s", err.Error())
		return err
	}

	eventsYaml, err := k8s.yamlEventsConverter.ConvertToYaml(eventList.Items)
	if err != nil {
		return err
	}

	podYaml = fmt.Sprintf("%s\n\nEvents:\n%s", podYaml, eventsYaml)

	podMap := pod.Object
	if spec, ok := podMap["spec"].(map[string]interface{}); ok {
		if nodeName, ok := spec["nodeName"].(string); ok {
			k8s.loggerToUse.Infof("Pod: %s, Node: %s", pod.GetName(), nodeName)
			podFileName := fmt.Sprintf("%s-%s", nodeName, pod.GetName())
			if err := k8s.writeToFileVar.writeToFile(podFileName, "pod", podYaml, saveFilesPath); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("error fetching node name for pod %s", pod.GetName())
		}
	} else {
		return fmt.Errorf("spec missing from pod")
	}
	return nil
}

// getLogsForPod fetches logs for all containers in a pod and writes them to files
func (k8s *k8sRetriever) getLogsForPod(namespace string, labelSelector string) error {
	saveFilesPath := k8s.outputPath + "/px-pods"
	err := k8s.fs.MkdirAll(saveFilesPath, 0766)
	if err != nil && os.IsExist(err) {
		k8s.loggerToUse.Infof("Directory already exists: %s", saveFilesPath)
	}
	if err != nil {
		k8s.loggerToUse.Errorf("error creating directory: %s", err.Error())
		panic(err.Error())
	}

	podList := &unstructured.UnstructuredList{}
	podList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "",
		Kind:    "PodList",
		Version: "v1",
	})

	labelMap, err := labels.ConvertSelectorToLabelsMap(labelSelector)
	if err != nil {
		return fmt.Errorf("error converting label selector to labels map: %v", err)
	}

	err = k8s.k8sClient.List(k8s.context, podList, client.InNamespace(namespace), client.MatchingLabels(labelMap))
	if err != nil {
		return fmt.Errorf("error retrieving pods: %s", err.Error())
	}

	for _, pod := range podList.Items {
		err := k8s.processPodForLogs(&pod, saveFilesPath)
		if err != nil {
			return err
		}
	}
	return nil
}

// processPodForLogs processes a single pod to fetch logs for its containers
func (k8s *k8sRetriever) processPodForLogs(pod *unstructured.Unstructured, saveFilesPath string) error {
	podMap := pod.Object

	spec, ok := podMap["spec"].(map[string]interface{})
	if !ok {
		return errors.New("spec missing from pod")
	}

	containers, ok := spec["containers"].([]interface{})
	if !ok {
		return errors.New("containers missing from pod")
	}

	for _, containerItem := range containers {
		k8s.processContainerForLogs(pod, containerItem, saveFilesPath)
	}

	return nil
}

// ProcessContainer processes a single container within a pod to fetch its logs
func (k8s *k8sRetriever) ProcessContainer(pod *unstructured.Unstructured, containerItem interface{}, saveFilesPath string) error {
	containerMap, ok := containerItem.(map[string]interface{})
	if !ok {
		return errors.New("error converting container to map")
	}

	containerName, ok := containerMap["name"].(string)
	if !ok {
		return errors.New("error fetching container name")
	}

	k8s.loggerToUse.Infof("Fetching logs for Container: %s", containerName)
	if err := k8s.logFetcher.FetchLogs(pod, containerName, saveFilesPath); err != nil {
		k8s.loggerToUse.Errorf("error fetching logs for container %s in pod %s: %v", containerName, pod.GetName(), err)
		return err
	}

	return nil
}

// FetchLogs fetches logs for a container in a pod using the RESTClient
func (k8s *k8sRetriever) FetchLogs(pod *unstructured.Unstructured, containerName string, saveFilesPath string) error {
	namespace, podName, nodeName, podCreationTimestamp, err := k8s.podDetailsExtractor.extractPodDetails(pod)
	if err != nil {
		return err
	}

	k8s.loggerToUse.Infof("Fetching logs for container %s in pod %s", containerName, podName)

	request, err := k8s.logRequestCreator.CreateLogsRequest(namespace, podName, containerName, podCreationTimestamp)
	if err != nil {
		return err
	}

	podLogs, err := request.DoRaw(k8s.context)
	if err != nil {
		k8s.loggerToUse.Errorf("error reading logs for container %s in pod %s: %v", containerName, podName, err)
		return err
	}

	str := string(podLogs)

	logFileName := fmt.Sprintf("%s-%s-%s", nodeName, podName, containerName)
	if err := k8s.writeToFileVar.writeToFile(logFileName, "logs", str, saveFilesPath, "log"); err != nil {
		k8s.loggerToUse.Errorf("error writing pod logs to file: %s", err.Error())
		return err
	}

	k8s.loggerToUse.Infof("Logs saved successfully for container %s in pod %s", containerName, podName)
	return nil
}

// extractPodDetails extracts details of a pod from the pod object
func (k8s *k8sRetriever) extractPodDetails(pod *unstructured.Unstructured) (namespace, podName, nodeName, podCreationTimestamp string, err error) {
	podMap := pod.Object

	namespace, _ = podMap["metadata"].(map[string]interface{})["namespace"].(string)
	if namespace == "" {
		return "", "", "", "", fmt.Errorf("failed to extract namespace from pod %s", pod.GetName())
	}

	podName = pod.GetName()
	nodeName, _ = podMap["spec"].(map[string]interface{})["nodeName"].(string)

	if ts, ok := podMap["metadata"].(map[string]interface{})["creationTimestamp"].(string); ok {
		podCreationTimestamp = ts
	}

	k8s.loggerToUse.Infof("Pod details extracted successfully for pod %s", podName)

	return namespace, podName, nodeName, podCreationTimestamp, nil
}

// CreateLogsRequest creates a RESTClient request to fetch logs for a container in a pod
func (k8s *k8sRetriever) CreateLogsRequest(namespace, podName, containerName, podCreationTimestamp string) (*rest.Request, error) {
	k8s.restConfig.GroupVersion = &schema.GroupVersion{
		Group:   "",
		Version: "v1",
	}
	k8s.restConfig.NegotiatedSerializer = scheme.Codecs.WithoutConversion()

	k8s.loggerToUse.Infof("Creating log request for pod %s", podName)

	realClient, err := rest.RESTClientFor(k8s.restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize REST client: %v", err)
	}

	k8s.restClient = realClient

	request := k8s.restClient.
		Get().
		AbsPath("/api/v1").
		Namespace(namespace).
		Resource("pods").
		Name(podName).
		SubResource("log").
		Param("container", containerName)

	if podCreationTimestamp != "" {
		request = request.Param("sinceTime", podCreationTimestamp)
	}

	k8s.loggerToUse.Infof("Log request created successfully for pod %s", podName)

	return request, nil
}

// listDeployments fetches all deployments in a namespace and writes them to files
func (k8s *k8sRetriever) listDeployments(namespace string) error {
	saveFilesPath := k8s.outputPath + "/px-deployments"
	if err := k8s.ensureDirectory(saveFilesPath); err != nil {
		return err
	}

	opts := []client.ListOption{client.InNamespace(namespace)}
	gvk := schema.GroupVersionKind{
		Group:   "apps",
		Kind:    "DeploymentList",
		Version: "v1",
	}

	deployments, err := k8s.fetchK8sObjects(gvk, namespace, opts)
	if err != nil {
		return fmt.Errorf("error retrieving deployments: %s", err.Error())
	}

	for _, deployment := range deployments.Items {
		err := k8s.handleDeploymentFunc.HandleDeployment(&deployment, saveFilesPath)
		if err != nil {
			return err
		}

	}

	return nil
}

// HandleDeployment processes a single deployment
func (k8s *k8sRetriever) HandleDeployment(obj *unstructured.Unstructured, saveFilesPath string) error {

	k8s.loggerToUse.Infof("real HandleDeployment called")

	// Check if the deployment object has a "spec"
	if _, exists := obj.Object["spec"]; !exists {
		return fmt.Errorf("deployment %s does not have a spec", obj.GetName())
	}

	deployYaml, err := k8s.yamlConverter.ConvertToYaml(obj)
	if err != nil {
		return err
	}
	k8s.loggerToUse.Infof("Deployment: %s", obj.GetName())
	if err := k8s.writeToFileVar.writeToFile(obj.GetName(), "deployment", deployYaml, saveFilesPath); err != nil {
		k8s.loggerToUse.Errorf("error writing deployment YAML to file: %s", err.Error())
		return err
	}
	return nil
}

// listStorageClusters fetches all storage clusters in a namespace and writes them to files
func (k8s *k8sRetriever) listStorageClusters(namespace string) error {
	saveFilesPath := k8s.outputPath + "/px-storageClusters"
	if err := k8s.ensureDirectory(saveFilesPath); err != nil {
		return err
	}

	opts := []client.ListOption{client.InNamespace(namespace)}
	gvk := schema.GroupVersionKind{
		Group:   "core.libopenstorage.org",
		Kind:    "StorageClusterList",
		Version: "v1",
	}

	storageClusters, err := k8s.fetchK8sObjects(gvk, namespace, opts)
	if err != nil {
		return fmt.Errorf("error retrieving storage clusters: %s", err.Error())
	}

	for _, storageCluster := range storageClusters.Items {
		err := k8s.handleStorageClusterFunc.HandleStorageCluster(&storageCluster, saveFilesPath)
		if err != nil {
			return err
		}
	}

	return nil

}

// HandleStorageCluster processes a single deployment
func (k8s *k8sRetriever) HandleStorageCluster(obj *unstructured.Unstructured, saveFilesPath string) error {

	k8s.loggerToUse.Infof("real HandleStorageClusters called")

	// Check if the deployment object has a "spec"
	if _, exists := obj.Object["spec"]; !exists {
		return fmt.Errorf("storage cluster %s does not have a spec", obj.GetName())
	}

	clusterYaml, err := k8s.yamlConverter.ConvertToYaml(obj)
	if err != nil {
		return err
	}
	k8s.loggerToUse.Infof("Storage Cluster: %s", obj.GetName())
	if err := k8s.writeToFileVar.writeToFile(obj.GetName(), "storagecluster", clusterYaml, saveFilesPath); err != nil {
		k8s.loggerToUse.Errorf("error writing storage cluster YAML to file: %s", err.Error())
		return err
	}
	return nil
}

// listKubernetesObjects fetches all kubernetes objects of a given type in a namespace and writes them to files
func (k8s *k8sRetriever) listKubernetesObjects(namespace string, gvk schema.GroupVersionKind, opts []client.ListOption, objectName string) error {
	saveFilesPath := k8s.outputPath + "/px-" + objectName + "s"
	if objectName == "storageclass" {
		saveFilesPath = k8s.outputPath + "/px-storage-classes"
	}
	if objectName == "vps" {
		saveFilesPath = k8s.outputPath + "/px-vps"
	}
	if objectName == "volumesnapshotclass" {
		saveFilesPath = k8s.outputPath + "/px-volumesnapshotclasses"
	}
	if err := k8s.ensureDirectory(saveFilesPath); err != nil {
		return err
	}

	kubernetesObjects, err := k8s.fetchK8sObjects(gvk, namespace, opts)
	if err != nil {
		return fmt.Errorf("error retrieving %ss: %s", objectName, err.Error())
	}

	for _, storageCluster := range kubernetesObjects.Items {
		err := k8s.handleKubernetesObjectsFunc.HandleKubernetesObjects(&storageCluster, saveFilesPath, objectName)
		if err != nil {
			return err
		}
	}

	return nil

}

// HandleKubernetesObjects processes a single deployment
func (k8s *k8sRetriever) HandleKubernetesObjects(obj *unstructured.Unstructured, saveFilesPath string, objectName string) error {

	noSpecExceptions := map[string]struct{}{
		"storageclass":        {},
		"vps":                 {},
		"storageprofile":      {},
		"k8s-event":           {},
		"volumesnapshotclass": {},
		"backuplocation":      {},
		"configmap":           {},
	}

	// Check if the object has a "spec", or is in the list of exceptions.
	if _, exists := obj.Object["spec"]; !exists {
		if _, isException := noSpecExceptions[objectName]; !isException {
			return fmt.Errorf("%s %s does not have a spec", objectName, obj.GetName())
		}
	}

	clusterYaml, err := k8s.yamlConverter.ConvertToYaml(obj)
	if err != nil {
		return err
	}
	k8s.loggerToUse.Infof("%s: %s", objectName, obj.GetName())
	if err := k8s.writeToFileVar.writeToFile(obj.GetName(), objectName, clusterYaml, saveFilesPath); err != nil {
		k8s.loggerToUse.Errorf("error writing %s YAML to file: %s", objectName, err.Error())
		return err
	}
	return nil
}

// retrieveMultipathConf retrieves the multipath.conf file from the given node and saves it to a file in the output directory
func (k8s *k8sRetriever) retrieveMultipathConf(nodeName string) error {
	sourcePath := "/etc/multipath.conf"

	// Read the source file content
	content, err := os.ReadFile(sourcePath)
	//check that the file exists if not return nil
	if err != nil && os.IsNotExist(err) {
		k8s.loggerToUse.Infof("File is not present %s", sourcePath)
		return nil
	}

	saveFilesPath := outputPath
	if err := k8s.ensureDirectory(saveFilesPath); err != nil {
		return err
	}

	k8s.loggerToUse.Infof("Writing multipath.conf to %s", saveFilesPath)
	if err := k8s.writeToFileVar.writeToFile("multipath", "config-file", string(content), saveFilesPath, ".conf"); err != nil {
		k8s.loggerToUse.Errorf("error writing storage cluster YAML to file: %s", err.Error())
		return err
	}

	k8s.loggerToUse.Infof("Successfully saved multipath config to %s", saveFilesPath)
	return nil
}
