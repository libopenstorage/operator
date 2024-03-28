# Portworx Kubernetes resources retriever

This is a simple Kubernetes client written in Go. It interacts with the Kubernetes API to retrieve information about Kubernetes resources like Deployments, Pods, and Services in a specific namespace.

## Prerequisites

- [Go 1.21](https://golang.org/dl/)

- [Kubernetes](https://kubernetes.io/docs/setup/)

- [Client-go](https://github.com/kubernetes/client-go)

## Description

The client can run either inside a Kubernetes cluster or outside of it. When running outside, it uses a `kubeconfig` file for authentication. When running inside a cluster, it uses the service account token provided by Kubernetes.

The client retrieves the list of Deployments, Pods, and Services, and saves them to individual YAML files.


## How to use

```bash
This tool retrieves Kubernetes objects and Portworx volume information.

Flags:
  -kubeconfig string
    	Path to the kubeconfig file. Specifies the path to the kubeconfig file to use for connection to the Kubernetes cluster.
  -namespace string
    	Portworx namespace. Specifies the Kubernetes namespace in which Portworx is running. (default "portworx")
  -output_dir string
    	Output directory path. Specifies the directory where output files will be stored. (default "/var/cores/px-k8s-retriever")

Examples:
  ./retriever --namespace portworx --kubeconfig /path/to/kubeconfig --output_dir /var/cores
  ./retriever --namespace kube-system --output_dir /tmp

Please specify --namespace, --kubeconfig (if connecting outside the cluster), and --output_dir when running the tool.
```

### Namespace is set to portworx
Namespace is set to `portworx`.

### Run the client with a Kubernetes Job

Set the `OUTPUT_PATH` environment variable to the directory where you want the YAML files to be stored. If `OUTPUT_PATH` is not set, the client defaults to "/var/cores".

For instance, you can set it in your terminal like this:

```bash
export OUTPUT_PATH=/path/to/output
```

Or you can set it in your Kubernetes job spec:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: px-k8s-retriever-job
spec:
  template:
    spec:
      serviceAccountName: portworx-operator
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  - key: px/enabled
                    operator: NotIn
                    values:
                      - 'false'
                  - key: node-role.kubernetes.io/master
                    operator: DoesNotExist
                  - key: node-role.kubernetes.io/control-plane
                    operator: DoesNotExist
              - matchExpressions:
                  - key: px/enabled
                    operator: NotIn
                    values:
                      - 'false'
                  - key: node-role.kubernetes.io/master
                    operator: Exists
                  - key: node-role.kubernetes.io/worker
                    operator: Exists
              - matchExpressions:
                  - key: px/enabled
                    operator: NotIn
                    values:
                      - 'false'
                  - key: node-role.kubernetes.io/control-plane
                    operator: Exists
                  - key: node-role.kubernetes.io/worker
                    operator: Exists
      containers:
        - name: k8s-retriever
          image: portworx/px-operator:latest
          env:
            - name: "OUTPUT_PATH"
              value: "/var/cores"
            - name: "PX_NAMESPACE"
              value: "portworx"
            - name: "NODE_NAME"
              valueFrom:
                fieldRef:
                  fieldPath: spec.nodeName
          command: ["/bin/sh", "-c", "/retriever --output_dir $OUTPUT_PATH --namespace $PX_NAMESPACE"]
          imagePullPolicy: Always
          securityContext:
            privileged: true
          volumeMounts:
            - name: log-volume
              mountPath: /var/cores/px-k8s-retriever
          resources:
            requests:
              cpu: 100m
              memory: 100Mi
            limits:
              cpu: 1500m
              memory: 2Gi
      restartPolicy: OnFailure
      volumes:
        - name: log-volume
          hostPath:
            path: /var/cores/px-k8s-retriever
            type: DirectoryOrCreate
```

Then you can run your job, and it will write the YAML files to the directory specified in the `OUTPUT_PATH` environment variable.


**NOTE:** Please make sure the directory is accessible and writable by your application. If you are running this as a Kubernetes job, you might need to mount a volume at the directory so that the files are stored on persistent storage.