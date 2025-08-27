.PHONY: all build docker-build-controller docker-build-plugin kind-up kind-load deploy demo clean undeploy kind-down
CONTROLLER_IMG ?= dra-controller:latest
PLUGIN_IMG ?= dra-plugin:latest
KIND_CLUSTER_NAME=mn-dra-poc

# Build the Go binaries for the controller and plugin
build:
	@echo ">> Building Go binaries..."
	go build -o bin/controller ./cmd/controller
	go build -o bin/plugin ./cmd/plugin

# Build the Docker image for the controller
docker-build-controller:
	@echo ">> Building controller image..."
	docker build -t ${CONTROLLER_IMG} -f Dockerfile.controller .

# Build the Docker image for the plugin
docker-build-plugin:
	@echo ">> Building plugin image..."
	docker build -t ${PLUGIN_IMG} -f Dockerfile.driver .

# Create a kind cluster using the specific config
kind-up:
	@echo ">> Creating Kind cluster '${KIND_CLUSTER_NAME}'..."
	kind create cluster --name ${KIND_CLUSTER_NAME} --config=./hack/kind-config.yaml

# Load the Docker image into the kind cluster
kind-load: docker-build-controller docker-build-plugin
	@echo ">> Loading Docker image '${IMG}' into Kind..."
	kind load docker-image ${CONTROLLER_IMG} --name ${KIND_CLUSTER_NAME}
	kind load docker-image ${PLUGIN_IMG} --name ${KIND_CLUSTER_NAME}

# Deploy all Kubernetes manifests to the cluster
deploy:
	@echo ">> Deploying Kubernetes manifests..."
	kubectl apply -f config/crd
	kubectl apply -f config/controller
	kubectl apply -f config/plugin

# Run the full demo from start to finish
demo: build kind-up kind-load deploy
	@echo ">> Running demo script..."
	@echo ">> Adding dummy0 interface to worker node..."
	docker exec ${KIND_CLUSTER_NAME}-worker ip link add dummy0 type dummy
	docker exec ${KIND_CLUSTER_NAME}-worker ip link set up dev dummy0
	@echo ">> Applying sample manifests..."
	kubectl apply -f config/samples
	@echo ">> Waiting for pod to be scheduled and running..."
	kubectl wait --for=condition=Ready pod/test-pod-using-podnetwork --timeout=120s
	@echo ">> Verifying pod is scheduled on the correct node (mn-dra-poc-worker)..."
	@kubectl get pod test-pod-using-podnetwork -o wide

# Remove all deployed Kubernetes manifests
undeploy:
	@echo ">> Deleting Kubernetes manifests..."
	kubectl delete -f config/plugin --ignore-not-found
	kubectl delete -f config/controller --ignore-not-found
	kubectl delete -f config/crd --ignore-not-found

# Delete the kind cluster
kind-down:
	@echo ">> Deleting Kind cluster '${KIND_CLUSTER_NAME}'..."
	kind delete cluster --name ${KIND_CLUSTER_NAME}

# Clean up local binaries
clean: kind-down
	@echo ">> Cleaning up local binaries..."
	rm -rf ./bin
