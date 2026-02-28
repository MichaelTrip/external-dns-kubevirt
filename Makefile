IMG ?= ghcr.io/michaeltrip/external-dns-kubevirt:latest

# Build the binary locally
.PHONY: build
build:
	go build -o bin/manager ./cmd/main.go

# Run unit tests
.PHONY: test
test:
	go test ./... -v -count=1

# Run go vet
.PHONY: vet
vet:
	go vet ./...

# Tidy and verify go modules
.PHONY: mod-tidy
mod-tidy:
	go mod tidy

# Build the Docker image
.PHONY: docker-build
docker-build:
	docker build -t $(IMG) .

# Push the Docker image
.PHONY: docker-push
docker-push:
	docker push $(IMG)

# Build and push in one step
.PHONY: docker-release
docker-release: docker-build docker-push

# Create the controller namespace
.PHONY: namespace
namespace:
	kubectl create namespace external-dns-kubevirt --dry-run=client -o yaml | kubectl apply -f -

# Deploy RBAC resources
.PHONY: deploy-rbac
deploy-rbac: namespace
	kubectl apply -f deploy/rbac.yaml

# Deploy the controller
.PHONY: deploy
deploy: deploy-rbac
	kubectl apply -f deploy/deployment.yaml

# Remove the controller and RBAC
.PHONY: undeploy
undeploy:
	kubectl delete -f deploy/deployment.yaml --ignore-not-found=true
	kubectl delete -f deploy/rbac.yaml --ignore-not-found=true

# Run the controller locally against the current kubeconfig cluster
.PHONY: run
run:
	go run ./cmd/main.go --leader-elect=false
