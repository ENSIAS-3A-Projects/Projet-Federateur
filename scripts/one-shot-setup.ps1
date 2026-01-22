# One-shot MBCAS setup for PowerShell
$ErrorActionPreference = "Stop"
$p = "mbcas"

# Delete existing minikube if it exists
minikube delete -p $p 2>$null

# Start minikube with feature gates
minikube start -p $p --cpus=4 --memory=4096 --driver=docker --feature-gates=InPlacePodVerticalScaling=true --extra-config=apiserver.feature-gates=InPlacePodVerticalScaling=true --extra-config=kubelet.feature-gates=InPlacePodVerticalScaling=true

# Enable metrics-server
minikube addons enable metrics-server -p $p | Out-Null

# Set Docker to minikube's daemon
minikube docker-env -p $p --shell=powershell | Invoke-Expression

# Build images
docker build -f Dockerfile.controller -t mbcas-controller:latest . --quiet
docker build -f Dockerfile.agent -t mbcas-agent:latest . --quiet

# Reset Docker context
Remove-Item Env:\DOCKER_TLS_VERIFY, Env:\DOCKER_HOST, Env:\DOCKER_CERT_PATH, Env:\MINIKUBE_ACTIVE_DOCKERD -ErrorAction SilentlyContinue

# Apply CRD first
kubectl apply -f k8s\mbcas\allocation.mbcas.io_podallocations.yaml
Start-Sleep -Seconds 2

# Apply namespace first (required for other resources)
kubectl apply -f k8s\mbcas\namespace.yaml

# Apply RBAC and config
kubectl apply -f k8s\mbcas\service_account.yaml
kubectl apply -f k8s\mbcas\role.yaml
kubectl apply -f k8s\mbcas\role_binding.yaml
kubectl apply -f k8s\mbcas\agent-rbac.yaml
kubectl apply -f config\agent\configmap.yaml

# Apply deployments
kubectl apply -f k8s\mbcas\controller-deployment.yaml
kubectl apply -f k8s\mbcas\agent-daemonset.yaml

# Wait for components
kubectl wait --for=condition=available --timeout=120s deployment/mbcas-controller -n mbcas-system 2>$null
kubectl wait --for=condition=ready --timeout=120s pod -l app.kubernetes.io/component=agent -n mbcas-system 2>$null

# Show status
kubectl get pods -n mbcas-system
