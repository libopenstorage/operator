# PX-K8S-Retriever Job

## Overview

The `px-k8s-retriever-job` is a Kubernetes Job designed to perform certain operations in the `/var/cores` directory on the nodes. This directory, by default, belongs to the root user.

We retrieve a bunch o Kubernetes objects useful to perform diagnostics and troubleshooting for Portworx.

## Permission Requirements

Because the target directory (`/var/cores`) is owned by the root user, the container within the `px-k8s-retriever-job` needs to run with root permissions to be able to create or modify files and subdirectories inside it. As such:

- The container is run in a privileged security context.
- The UID and GID for the operations are set to 0 (root).

## Usage

To deploy the job, create it on the namespace where you've deployed Portworx:

```bash
scripts % kubectl apply -f px-k8s-retriever-job.yaml -n portworx
```

Check the logs, you can see on which of your nodes the job ran:

```bash
scripts % kubectl logs job/px-k8s-retriever-job -n portworx

time="2023-10-06 09:13:48" level=info msg="---Starting Kubernetes client!---"
time="2023-10-06 09:13:48" level=info msg="Job is running on Kubernetes Node: worker0"
time="2023-10-06 09:13:48" level=info msg="Creating YAML files for all resources in namespace: kube-system"
time="2023-10-06 09:13:48" level=info msg="YAML files will be created in: /var/cores/px-k8s-retriever"
time="2023-10-06 09:13:48" level=info msg="Deployment: autopilot"
time="2023-10-06 09:13:48" level=info msg="Deployment: portworx-operator"
...
```
