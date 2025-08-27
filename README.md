# Multi Network using DRA

**Note:** This project is experimental and under active development. It is not recommended for production use.

This project is a proof-of-concept (PoC) demonstrating how to manage multiple network interfaces for Pods in Kubernetes using Dynamic Resource Allocation (DRA) and a Node Resource Interface (NRI) plugin.

It introduces a `PodNetwork` Custom Resource Definition (CRD) to represent a logical network within the cluster. A controller manages the lifecycle of these `PodNetwork` objects, while a DRA/NRI plugin running on each node is responsible for allocating network devices and attaching them to Pods.

## Key Concepts

This solution is built upon the interaction between a new `PodNetwork` Custom Resource Definition (CRD), the existing Dynamic Resource Allocation (DRA) framework, and a custom DRA driver.

### PodNetwork CRD
The PodNetwork is a cluster-scoped, vendor-neutral Custom Resource that represents a logical network in the cluster. Its primary purpose is to act as a centralized, administrative handle for network configuration. Cluster administrators define PodNetwork objects to govern the types of networks that workloads can connect to, and these objects serve as input for the DRA driver.

More details about the definition can be found [here](https://docs.google.com/document/d/1M1uw1YQVwXf73rUmZyUtQCDBNA9BY8tmtWs0PB0E_U8/edit?usp=sharing)

### Dynamic Resource Allocation (DRA)
DRA is the underlying Kubernetes API framework used for requesting and assigning resources to Pods. This PoC leverages several DRA components:
- ResourceSlice: The DRA driver creates ResourceSlice objects to advertise available network devices on a node. Per the design, each device is marked with a special podNetwork attribute that links it to a specific PodNetwork object.
- ResourceClaim: A user requests a network interface by creating a ResourceClaim. The claim uses selectors to target a device that has the desired podNetwork attribute, effectively requesting an interface from a specific PodNetwork.
- DeviceClass: This DRA object can be used to simplify the user experience by pre-configuring the selectors for a specific type of network, so users don't have to write complex CEL expressions in their ResourceClaims.
- DRA Driver: The DRA driver is the provider-specific component that implements the logic for network management. It runs on the nodes and is responsible for:
    - Reading PodNetwork objects defined by the administrator.
    - Advertising available physical or virtual network interfaces via ResourceSlice objects.
    - Handling requests from the Kubelet to attach a network interface to a Pod's namespace when a ResourceClaim has been approved.

## How It Works

1.  **PodNetwork CRD**: A cluster administrator defines a `PodNetwork` resource to represent a logical network.
2.  **ResourceClaim**: A user creates a `ResourceClaim` to request a network interface from a specific `PodNetwork`.
3.  **Pod Consumption**: A Pod is created that references the `ResourceClaim`.
4.  **DRA/NRI Plugin**: The plugin, running on the node where the Pod is scheduled, receives the request from the Kubelet.
5.  **Network Attachment**: The plugin's NRI component moves a network interface from the host into the Pod's network namespace, making it available to the application.


## Prerequisites

To run this PoC, you will need the following tools installed:

-   [Docker](https://docs.docker.com/get-docker/)
-   [Go](https://golang.org/doc/install) (version 1.24 or higher)
-   [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) (version 0.29 or higher)
-   [Kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl/)

## Getting Started

The easiest way to get started is to run the `demo` target in the `Makefile`. This single command will:

1.  Build the controller and plugin binaries.
2.  Create a new KIND cluster.
3.  Build the container images for the controller and plugin and load them into the KIND cluster.
4.  Deploy the `PodNetwork` CRD, controller, and plugin to the cluster.
5.  Create a `dummy0` network interface on the KIND control plane node for testing.
6.  Deploy sample resources, including a `DeviceClass`, `ResourceClaim`, and a sample `Pod`.

To run the demo, execute the following command:

```bash
make demo
```

For a more detailed step-by-step guide, follow the [User Guide Section](#user-guide)

## Cleanup

To clean up the resources created by the demo, you can use the following command:
```bash
make clean
```

## User Guide

This guide provides a complete, step-by-step walkthrough of how to use the multi-network DRA plugin. We will start by creating a new local Kubernetes cluster, build and deploy the necessary components, and finish by attaching a second network interface to a sample Pod.

### Step 1: Set Up a KIND Cluster with DRA Enabled
Dynamic Resource Allocation is a feature that must be explicitly enabled in the cluster. For a sample kind condig file refer to [this file](hack/kind-config.yaml)

Create the KIND cluster using this command:
```bash
kind create cluster --config=hack/kind-config.yaml --name mn-dra-poc
```

### Step 2: Install the PodNetwork CRD
PodNetwork CRD tells Kubernetes about the definition of the PodNetwork object.
```bash
kubectl apply -f config/crd
```

### Step 3: Install the PodNetwork Controller
So far, Kubernetes understands the PodNetwork object definition but has no way to manage it's state. For that, we install a controller that can listen for any PodNetwork object events and update the object accordingly. Currently Podnetwork has 2 keys that determine if it is ready to be used: `enabled` and `Status`. Unless the PodNetwork is ready, no devices in the ResourceSlice will be linked to it.

To build and load the controller image in the kind cluster, run:
```bash
docker build -t dra-controller:latest -f Dockerfile.controller .
kind load docker-image dra-controller:latest --name mn-dra-poc
```

Then create the controller Role and DaemonSet using
```bash
kubectl apply -f config/controller
```

The controller also needs permissions to watch and modify PodNetwork objects. They are included in the above mentioned folder.

Verify that the controller-manager pod is ready and running using:
```bash
kubectl get pods -n system
```
The output should be similar to:
```bash
NAME                                  READY   STATUS    RESTARTS   AGE
controller-manager-64696c86b6-wt48p   1/1     Running   0          85s
```

### Step 4: Install the DRA driver and NRI plugin
Note: The code in this repo uses a single binary and container image for the DRA driver and NRI plugin for simplicity. 

To build and load the plugin image in the kind cluster, run:
```bash
docker build -t dra-plugin:latest -f Dockerfile.controller .
kind load docker-image dra-plugin:latest --name mn-dra-poc
```

For the DRA driver to advertise ResourceSlice objects on all nodes, it needs to run as a DaemonSet. Similarly, the NRI plugin needs to plug in to the pod lifecycle and move network interfaces between the host and the pod so it also needs to run on all the nodes (preferably as a DaemonSet)
```bash
kubectl apply -f config/plugin
```

Verify that the dra-plugin pods are ready and running using:
```bash
kubectl get pods -A | grep "dra-plugin"
```
The output should be similar to:
```bash
kube-system          dra-plugin-24g6n                                   1/1     Running   0          96s
kube-system          dra-plugin-5vnxc                                   1/1     Running   0          96s
kube-system          dra-plugin-r5vkt                                   1/1     Running   0          96s
```
### Step 5: Attach Network Interfaces
Currently this POC supports filtering network interfaces using their name but this can be extended to support other parameters.

In this demo, we will add dummy interfaces on one of the nodes. In real world scenarios, you will attach NICs connected to different networks to the VM.
```bash
docker exec mn-dra-poc-worker ip link add dummy0 type dummy
docker exec mn-dra-poc-worker ip link set up dev dummy0
```
So far these interfaces will be advertised as any other interface on the node in the ResourceSlice. Only when we create a new PodNetwork will the driver start to add more attributes to relevant devices

To verify that this dummy0 interface is advertised, we can check the resourceslice on the worker node.

Note: It may take a few seconds for the resourceslice to pick this up



### Step 6: Create PodNetwork

TODO

### Step 7: Create ResourceClaim and Pod

TODO

## Development

For development purposes, you can use the individual `make` targets to run each step of the process manually.

-   `make build`: Build the Go binaries.
-   `make docker-build-controller`: Build the controller Docker image.
-   `make docker-build-plugin`: Build the plugin Docker image.
-   `make kind-up`: Create the KIND cluster.
-   `make kind-load`: Load the Docker images into the cluster.
-   `make deploy`: Deploy the Kubernetes manifests.
