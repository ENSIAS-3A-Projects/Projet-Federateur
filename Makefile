.PHONY: all build test docker-build docker-push deploy clean setup-minikube load-images up

VERSION ?= latest
IMG_AGENT = mbcas-agent:$(VERSION)
IMG_CONTROLLER = mbcas-controller:$(VERSION)

all: build test

build:
	go build -o bin/agent ./cmd/agent
	go build -o bin/controller ./cmd/controller

test:
	go test -v ./...

setup-minikube:
	minikube start --feature-gates=InPlacePodVerticalScaling=true
	minikube addons enable metrics-server

docker-build:
	docker build -t $(IMG_AGENT) -f Dockerfile.agent .
	docker build -t $(IMG_CONTROLLER) -f Dockerfile.controller .

load-images:
	minikube image load $(IMG_AGENT)
	minikube image load $(IMG_CONTROLLER)

deploy:
	kubectl apply -k config/crd/
	kubectl apply -f config/namespace.yaml
	kubectl apply -k config/rbac/
	kubectl apply -k config/agent/
	kubectl apply -k config/controller/

up: build setup-minikube docker-build load-images deploy

clean:
	rm -rf bin/
