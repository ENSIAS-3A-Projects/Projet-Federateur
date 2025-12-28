# Makefile for MBCAS project

.PHONY: manifests generate fmt vet test

# Generate manifests e.g. CRD, RBAC etc.
manifests:
	controller-gen rbac:roleName=manager-role crd webhooks paths="./api/..." output:crd:artifacts:config=config/crd/bases

# Generate code
generate:
	controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

# Run go fmt against code
fmt:
	go fmt ./...

# Run go vet against code
vet:
	go vet ./...

# Run tests
test:
	go test ./... -v -coverprofile cover.out

# Install controller-gen if not present
controller-gen:
ifeq (, $(shell which controller-gen))
	@{ \
	set -e ;\
	CONTROLLER_GEN_TMP_DIR=$$(mktemp -d) ;\
	cd $$CONTROLLER_GEN_TMP_DIR ;\
	go mod init tmp ;\
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest ;\
	rm -rf $$CONTROLLER_GEN_TMP_DIR ;\
	}
CONTROLLER_GEN=$(shell go env GOPATH)/bin/controller-gen
else
CONTROLLER_GEN=$(shell which controller-gen)
endif
