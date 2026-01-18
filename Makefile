.PHONY: all build test docker-build docker-push deploy clean

VERSION ?= v0.1.0
REGISTRY ?= localhost:5000

all: build test

build:
	go build -o bin/agent ./cmd/agent
	go build -o bin/controller ./cmd/controller
	go build -o bin/eval-service ./apps/eval-service

test:
	go test -v ./...

docker-build:
	docker build -t $(REGISTRY)/mbcas-agent:$(VERSION) -f Dockerfile.agent .
	docker build -t $(REGISTRY)/mbcas-controller:$(VERSION) -f Dockerfile.controller .
	docker build -t $(REGISTRY)/eval-service:$(VERSION) -f apps/eval-service/Dockerfile ./apps/eval-service

docker-push:
	docker push $(REGISTRY)/mbcas-agent:$(VERSION)
	docker push $(REGISTRY)/mbcas-controller:$(VERSION)
	docker push $(REGISTRY)/eval-service:$(VERSION)

deploy:
	kubectl apply -f config/crd/
	kubectl apply -f config/rbac/
	kubectl apply -f config/controller/

clean:
	rm -rf bin/
