#!/bin/bash
set -euo pipefail

# =========================================
# Configuration
# =========================================
NAMESPACE="mbcas-test"
DURATION_SECONDS=300
SAMPLE_INTERVAL=5
OUTPUT_FILE="mbcas_multi_pod_metrics_$(date +%Y%m%d_%H%M%S).json"

# name|request|limit|type
PODS=(
  "idle-overprov|500m|1000m|idle"
  "low-cpu|100m|500m|cpu"
  "med-cpu|300m|1000m|cpu"
  "bursty-cpu|100m|2000m|bursty"
)

LOAD_POD="load-generator"

declare -A LOAD_PROFILE_C
declare -A LOAD_PROFILE_T
declare -A LOAD_PROFILE_MODE

LOAD_PROFILE_C["low-cpu"]=20
LOAD_PROFILE_T["low-cpu"]=4
LOAD_PROFILE_MODE["low-cpu"]="constant"

LOAD_PROFILE_C["med-cpu"]=60
LOAD_PROFILE_T["med-cpu"]=4
LOAD_PROFILE_MODE["med-cpu"]="constant"

LOAD_PROFILE_C["bursty-cpu"]=120
LOAD_PROFILE_T["bursty-cpu"]=3
LOAD_PROFILE_MODE["bursty-cpu"]="burst"

# =========================================
# Helpers
# =========================================

to_millicores() {
  local v="${1:-}"
  if [[ -z "$v" || "$v" == "N/A" ]]; then
    echo 0; return
  fi
  if [[ "$v" =~ m$ ]]; then
    echo "${v%m}"
    return
  fi
  awk -v x="$v" 'BEGIN{printf "%d\n", (x*1000)}' 2>/dev/null || echo 0
}

# =========================================
# Setup
# =========================================

echo "Creating namespace..."
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl label namespace "${NAMESPACE}" mbcas.io/managed=true --overwrite >/dev/null

# =========================================
# Deploy Pods
# =========================================

echo "Deploying workload..."

MANIFEST="$(mktemp)"

{
echo "apiVersion: v1"
echo "kind: List"
echo "items:"

for entry in "${PODS[@]}"; do
  IFS="|" read -r NAME REQ LIM TYPE <<<"$entry"

cat <<EOF
- apiVersion: v1
  kind: Pod
  metadata:
    name: ${NAME}
    namespace: ${NAMESPACE}
    labels:
      app: ${NAME}
      mbcas.io/managed: "true"
      mbcas.test/type: "${TYPE}"
  spec:
    containers:
    - name: app
EOF

if [[ "$TYPE" == "idle" ]]; then
cat <<EOF
      image: busybox
      imagePullPolicy: IfNotPresent
      command: ["/bin/sh","-c","while true; do sleep 3600; done"]
EOF
else
cat <<EOF
      image: registry.k8s.io/e2e-test-images/agnhost:2.40
      imagePullPolicy: IfNotPresent
      args: ["netexec","--http-port=8080"]
      ports:
      - containerPort: 8080
EOF
fi

cat <<EOF
      resources:
        requests:
          cpu: "${REQ}"
        limits:
          cpu: "${LIM}"
    restartPolicy: Always
EOF

if [[ "$TYPE" != "idle" ]]; then
cat <<EOF
- apiVersion: v1
  kind: Service
  metadata:
    name: ${NAME}
    namespace: ${NAMESPACE}
  spec:
    selector:
      app: ${NAME}
    ports:
    - port: 8080
      targetPort: 8080
EOF
fi

done

# Fortio pod
cat <<EOF
- apiVersion: v1
  kind: Pod
  metadata:
    name: ${LOAD_POD}
    namespace: ${NAMESPACE}
  spec:
    containers:
    - name: fortio
      image: fortio/fortio
      imagePullPolicy: IfNotPresent
      command: ["fortio"]
      args: ["server"]
EOF

} > "$MANIFEST"

kubectl apply -f "$MANIFEST" >/dev/null
rm -f "$MANIFEST"


# =========================================
# Wait for readiness
# =========================================

echo "Waiting for pods..."
kubectl wait --for=condition=Ready pod --all -n "${NAMESPACE}" --timeout=180s >/dev/null


echo "Waiting for Fortio..."
until kubectl exec -n "${NAMESPACE}" "${LOAD_POD}" -- fortio help >/dev/null 2>&1
do
  sleep 2
done


echo "Waiting for metrics-server..."
until kubectl top nodes >/dev/null 2>&1
do
  sleep 3
done


echo "All systems ready."


# =========================================
# Init JSON
# =========================================

START_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

cat > "${OUTPUT_FILE}" <<EOF
{
  "test_name": "MBCAS Multi Pod Evaluation",
  "namespace": "${NAMESPACE}",
  "start_time": "${START_TIME}",
  "samples": [
EOF


# =========================================
# Fortio
# =========================================

run_fortio() {

  local svc="$1"
  local c="$2"
  local t="$3"

  kubectl exec -n "${NAMESPACE}" "${LOAD_POD}" -- \
    fortio load -quiet -json - \
    -c "${c}" -t "${t}s" -p "50,95,99" \
    "http://${svc}:8080" 2>/dev/null || true
}


extract_ms() {

python3 - <<PY 2>/dev/null
import json,sys
try:
 d=json.loads(sys.stdin.read())
 for p in d["DurationHistogram"]["Percentiles"]:
  if p["Percentile"]==${1}:
   print(round(p["Value"]*1000,2))
   break
 else: print(0)
except: print(0)
PY
}


# =========================================
# Main Loop
# =========================================

ITERATIONS=$((DURATION_SECONDS / SAMPLE_INTERVAL))
FIRST=1

for i in $(seq 1 "${ITERATIONS}"); do

  TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  ELAPSED=$((i * SAMPLE_INTERVAL))

  for entry in "${PODS[@]}"; do

    IFS="|" read -r NAME _ _ TYPE <<<"$entry"

    REQUEST="$(kubectl get pod "$NAME" -n "${NAMESPACE}" -o jsonpath='{.spec.containers[0].resources.requests.cpu}' 2>/dev/null || echo N/A)"
    LIMIT="$(kubectl get pod "$NAME" -n "${NAMESPACE}" -o jsonpath='{.spec.containers[0].resources.limits.cpu}' 2>/dev/null || echo N/A)"


    if kubectl top pod "$NAME" -n "${NAMESPACE}" >/dev/null 2>&1; then
      USAGE="$(kubectl top pod "$NAME" -n "${NAMESPACE}" --no-headers | awk '{print $2}')"
    else
      USAGE="N/A"
    fi


    PODALLOC_LIM="N/A"
    PODALLOC_STATUS="N/A"

    if kubectl get podallocation "$NAME" -n "${NAMESPACE}" &>/dev/null; then
      PODALLOC_LIM="$(kubectl get podallocation "$NAME" -n "${NAMESPACE}" -o jsonpath='{.spec.desiredCPULimit}')"
      PODALLOC_STATUS="$(kubectl get podallocation "$NAME" -n "${NAMESPACE}" -o jsonpath='{.status.phase}')"
    fi


    P50=0
    P95=0

    if [[ "$TYPE" != "idle" ]]; then

      RAW="$(run_fortio "$NAME" "${LOAD_PROFILE_C[$NAME]}" "${LOAD_PROFILE_T[$NAME]}")"

      if [[ -n "$RAW" ]]; then
        P50="$(echo "$RAW" | extract_ms 50)"
        P95="$(echo "$RAW" | extract_ms 95)"
      fi
    fi


    if [[ "$FIRST" -eq 0 ]]; then
      echo "," >> "${OUTPUT_FILE}"
    fi
    FIRST=0


cat >> "${OUTPUT_FILE}" <<EOF
    {
      "timestamp": "${TS}",
      "elapsed": ${ELAPSED},
      "pod": "${NAME}",
      "request": "${REQUEST}",
      "limit": "${LIMIT}",
      "usage": "${USAGE}",
      "request_m": $(to_millicores "$REQUEST"),
      "limit_m": $(to_millicores "$LIMIT"),
      "usage_m": $(to_millicores "$USAGE"),
      "podalloc_limit": "${PODALLOC_LIM}",
      "podalloc_status": "${PODALLOC_STATUS}",
      "p50_ms": ${P50},
      "p95_ms": ${P95}
    }
EOF


    printf "%03ds | %-12s | %5s | %6s | %6s | %6sms | %6sms | %s\n" \
      "$ELAPSED" "$NAME" "$REQUEST" "$LIMIT" "$USAGE" "$P50" "$P95" "$PODALLOC_STATUS"

  done

  sleep "${SAMPLE_INTERVAL}"

done


# =========================================
# Finalize
# =========================================

END="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

cat >> "${OUTPUT_FILE}" <<EOF

  ],
  "end_time": "${END}"
}
EOF


echo ""
echo "Finished."
echo "Results: $OUTPUT_FILE"
echo ""

echo "Pods:"
kubectl get pods -n "${NAMESPACE}"

echo ""
echo "Cleanup:"
echo " kubectl delete namespace ${NAMESPACE}"
