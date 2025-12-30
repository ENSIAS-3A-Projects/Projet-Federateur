# AI Coding Agent Instructions

This workspace contains two distinct projects: **MBCAS** (Kubernetes CPU allocation system) and **UrbanMoveMS** (microservices transport platform).

## Project Structure

### MBCAS — Market-Based CPU Allocation System
Located in the workspace root. A Kubernetes operator that dynamically adjusts pod CPU limits based on kernel throttling signals using game theory (Fisher market proportional fairness).

**Architecture (3 components):**
1. **Agent** (`pkg/agent/`, `cmd/agent/`) — DaemonSet that reads cgroup throttling, computes fair allocations, writes `PodAllocation` CRDs
2. **Controller** (`pkg/controller/`, `cmd/controller/`) — Deployment that watches `PodAllocation` CRDs, applies in-place pod CPU limit updates
3. **CRD** (`api/v1alpha1/podallocation_types.go`) — Custom resource declaring desired CPU allocations

**Key invariants:**
- Agent runs as **root** (needs cgroup access) — see `config/agent/daemonset.yaml` securityContext
- Controller runs as **non-root** (uses Kubernetes API only)
- Uses **in-place pod vertical scaling** (requires `InPlacePodVerticalScaling` feature gate)
- CRDs are **namespaced** (not cluster-scoped) to align with pod lifecycle
- Market solver (`pkg/allocation/market.go`) returns **int64 millicores** for deterministic rounding

### UrbanMoveMS — Urban Transport Microservices
Located in `Software-Oriented-Application/`. Microservices platform for urban transit (users, tickets, routes, buses, notifications).

**Services (Spring Boot/Java 17 except Gateway):**
- `bus-service/` — Bus fleet & geolocation (Maven, Spring Boot 3.5.7)
- `trajet-service/` — Routes & schedules
- `ticket-service/` — Ticket purchases
- `UserService/` — User management
- `gateway/` — Spring Cloud Gateway (Gradle, Spring Boot 3.2.3)

**Infrastructure:**
- PostgreSQL per service (separate databases)
- Kafka for inter-service messaging (Zookeeper + Kafka broker)
- Docker Compose orchestration (`docker-compose.yml`)
- Kubernetes manifests in `k8s/`

## Critical Workflows

### MBCAS Development

**Build & Test (PowerShell):**
```powershell
# Full MVP deployment to Minikube
.\scripts\mvp-test.ps1 all

# Individual steps
.\scripts\mvp-test.ps1 cluster    # Create cluster with feature gate
.\scripts\mvp-test.ps1 build      # Build Docker images
.\scripts\mvp-test.ps1 deploy     # Apply manifests
.\scripts\mvp-test.ps1 verify     # Check deployment
```

**Generate Kubernetes manifests after API changes:**
```bash
make manifests  # Regenerates CRDs in config/crd/bases/
make generate   # Regenerates deepcopy for v1alpha1
```

**Run tests:**
```bash
make test  # Runs all Go tests with coverage
```

**Manual pod CPU testing:**
```bash
go run main.go list -n default          # List pods
go run main.go usage -n default         # Show CPU usage/throttling
go run main.go update -n default <pod>  # Force resize
```

### UrbanMoveMS Development

**Build images for Minikube (PowerShell):**
```powershell
.\Software-Oriented-Application\build-local.ps1  # Builds all services into Minikube's Docker
```

**Deploy to Kubernetes:**
```powershell
.\Software-Oriented-Application\deploy.ps1
```

**Local Docker Compose development:**
```bash
cd Software-Oriented-Application
docker-compose up -d  # Starts all services + databases + Kafka
```

## Code Conventions

### MBCAS (Go)

**Package organization:**
- `pkg/agent/` — Node-level components (cgroup reader, demand tracker)
- `pkg/controller/` — Cluster-level reconciliation
- `pkg/allocation/` — Core market solver (game theory)
- `pkg/actuator/` — Pod resize operations
- `cmd/{agent,controller}/` — Entrypoints (minimal, delegates to pkg/)

**Naming patterns:**
- Constants use `CamelCase` with descriptive names (e.g., `ResizeCooldown`, `MaxStepSizeFactor`)
- Test files have extensive table-driven tests (see `pkg/allocation/market_test.go` — 529 lines)
- Error messages use `fmt.Errorf` with `%w` for wrapping (Go 1.13+ convention)

**Critical implementation details:**
- **Cgroup paths:** Use glob patterns in `pkg/agent/cgroup/reader.go` (may fail on non-containerd CRIs)
- **EMA smoothing:** Fast increase (α=0.3), slow decrease (α=0.05) in `pkg/agent/demand/tracker.go`
- **Safety limits:** Controller enforces 30s cooldown, 1.5x max step size per resize
- **Guaranteed QoS pods:** Explicitly skipped by controller (see `isGuaranteedQoS()` in `podallocation_controller.go`)

**Testing philosophy:**
- Core logic (`allocation`, `actuator`) has ~50% test-to-code ratio
- Integration tests use real Kubernetes (Minikube/Kind), not mocks
- Failure modes documented in test cases (e.g., `market_test.go` has 21 edge cases)

### UrbanMoveMS (Java/Spring)

**Service structure:**
- Each service is **independently deployable** (own Dockerfile, database, port)
- REST APIs (Spring Web) for synchronous calls
- Kafka for async events (e.g., ticket purchase notifications)

**Database per service pattern:**
- `postgres-bus` (port 5434), `postgres-ticket` (5433), `postgres-trajet` (5435), `postgres-user` (5432)
- Schema migrations: Not automated (manual SQL scripts)

**Gateway routing:**
- Spring Cloud Gateway (`gateway/`) centralized routing
- JWT authentication (jjwt 0.11.5)
- Routes configured in `application.yml` (not visible in structure snapshot)

## Integration Points

### MBCAS ↔ Kubernetes API
- **Agent writes:** `PodAllocation` CRDs via dynamic client (`pkg/agent/writer.go`)
- **Controller reads:** Pods + PodAllocations via controller-runtime client
- **Controller writes:** Pod resize subresource (`PATCH /api/v1/namespaces/{ns}/pods/{name}/resize`)

### UrbanMoveMS ↔ External Services
- **Kafka topics:** Services publish/subscribe to domain events (topic names not documented — check `application.properties` in each service)
- **Database isolation:** No direct DB-to-DB calls (enforced by network isolation in Kubernetes)

## Common Pitfalls

### MBCAS
1. **Cgroup path assumptions:** Agent expects `/sys/fs/cgroup/kubepods.slice/...` hierarchy (containerd). May break on CRI-O or custom configurations.
2. **Feature gate:** In-place resizing requires `InPlacePodVerticalScaling=true` on kubelet + API server. Minikube script sets this via `--extra-config`.
3. **Root privileges:** Agent DaemonSet MUST run as root (UID 0) despite security best practices — cgroup reads require host filesystem access.
4. **Namespace mismatch:** Agent/controller deploy to `mbcas-system` namespace (not `default`). CRDs must be cluster-scoped for discovery.

### UrbanMoveMS
1. **Port conflicts:** Each PostgreSQL uses unique port (5432-5435). Docker Compose assigns host ports, Kubernetes uses ClusterIP.
2. **Build tool mix:** Bus/Ticket/Trajet use Maven; Gateway uses Gradle. Build scripts must handle both.
3. **Image tagging:** `build-local.ps1` tags images as `chaditaqi/<service>:latest` — must match deployment YAMLs.
4. **Kafka startup order:** Zookeeper must be healthy before Kafka starts (see `depends_on` in docker-compose.yml healthchecks).

## Quick Reference

**MBCAS verification commands:**
```bash
kubectl get podallocations -A -o wide               # Check allocations
kubectl -n mbcas-system logs ds/mbcas-agent -f      # Watch agent logs
kubectl -n mbcas-system logs deploy/mbcas-controller -f  # Watch controller
```

**UrbanMoveMS service endpoints (Docker Compose):**
```
Gateway:    localhost:8888
Bus:        localhost:8081
Ticket:     localhost:8082
Trajet:     localhost:8083
User:       localhost:8084
Kafka:      localhost:9092
```

**Module imports:**
- MBCAS: `import "mbcas/pkg/..."` (see `go.mod`)
- Controller-runtime version: `v0.17.0` (pinned)
- Kubernetes API: `v0.29.0` (Go 1.21 required)

## When to Ask for Clarification

- **MBCAS PSI integration:** Not yet implemented (mentioned in comments but no code)
- **UrbanMoveMS API contracts:** Service-to-service API specs not documented
- **Load testing results:** README mentions testing but no benchmark data in repo
- **Production readiness:** MBCAS explicitly marked as MVP (not production-ready)
