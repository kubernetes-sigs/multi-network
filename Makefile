# Copyright 2026
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

REGISTRY ?= ghcr.io/kubernetes-sigs
VERSION ?= $(shell git describe --dirty --tags --always 2>/dev/null)

all: verify build-image

.PHONY: verify
verify:
	hack/verify-all.sh

.PHONY: test
test:
	@mkdir -p _output
	go test $$(go list ./... | grep -v /test/) -race -count=1 -coverprofile=_output/coverage.out

.PHONY: test-e2e
test-e2e:
	go test ./test/e2e/... -v -count=1 -ginkgo.v $(E2E_ARGS)

.PHONY: test-conformance
test-conformance:
	go test ./test/conformance/... -v -count=1 -ginkgo.v $(CONFORMANCE_ARGS)

.PHONY: install-crds
install-crds:
	@go mod download sigs.k8s.io/multi-network-api
	@API_DIR=$$(go list -m -f '{{.Dir}}' sigs.k8s.io/multi-network-api); \
	if [ -z "$$API_DIR" ] || [ ! -d "$$API_DIR/deployment" ]; then \
		echo "failed to find CRD deployment directory for sigs.k8s.io/multi-network-api" >&2; \
		exit 1; \
	fi; \
	kubectl apply -f "$${API_DIR}/deployment"

.PHONY: .build-image
build-image:
	docker build -t multi-network-controller:$(VERSION) -f ./build/multi-network-controller/Dockerfile .

.PHONY: push-image
push-image: build-image
	docker tag multi-network-controller:$(VERSION) $(REGISTRY)/multi-network-controller:$(VERSION)
	docker push $(REGISTRY)/multi-network-controller:$(VERSION)