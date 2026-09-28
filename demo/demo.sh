#!/bin/bash
set -e

CLUSTER_NAME="nishant-demo"
NODE_NAME="${CLUSTER_NAME}-control-plane"

# Colorful output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

pecho() {
  echo -e "\n${BLUE}==>${NC} ${GREEN}$1${NC}"
}

pecho "Checking if cluster $CLUSTER_NAME exists..."
if ! kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
  echo "Creating kind cluster..."
  kind create cluster --name $CLUSTER_NAME --image kindest/node:v1.35.0 > /dev/null 2>&1
  make docker-build IMG=controller:demo > /dev/null 2>&1
  kind load docker-image controller:demo --name $CLUSTER_NAME > /dev/null 2>&1
  make install > /dev/null 2>&1
  make deploy IMG=controller:demo > /dev/null 2>&1
  kubectl wait --for=condition=available --timeout=60s deployment/k8s-nodepool-controller-controller-manager -n k8s-nodepool-controller-system > /dev/null 2>&1
fi

sleep 1
pecho "1. Creating NodePool 'gpu-pool' with labels, taints, and UnreadyPolicy (Action=Remove, GracePeriod=5s)"
cat <<YAML | kubectl apply -f -
apiVersion: nodemanager.example.org.example.org/v1
kind: NodePool
metadata:
  name: gpu-pool
spec:
  labels:
    accelerator: nvidia-tesla-t4
  taints:
  - key: gpu
    value: "true"
    effect: NoSchedule
  unreadyPolicy:
    gracePeriod: 5s
    action: Remove
    maxConcurrentRemovals: 2
YAML

sleep 2
pecho "2. Simulating node join: applying bootstrap label to $NODE_NAME"
kubectl label node $NODE_NAME nodes.example.com/nodepool=gpu-pool --overwrite

sleep 2
pecho "3. Verifying the controller applied the declared labels and taints via SSA"
kubectl get node $NODE_NAME --show-labels | grep -o 'accelerator=[^, ]*' || true
kubectl get node $NODE_NAME -o jsonpath='{.spec.taints}' | grep gpu || true

sleep 3
pecho "4. Simulating infra reclaim: stopping kubelet and marking node as NotReady"
# Stop kubelet so it doesn't fight the patch
docker exec $NODE_NAME systemctl stop kubelet
# Patch status to mimic a dead node that expired its grace period
kubectl patch node $NODE_NAME --subresource=status --type=merge -p '{"status":{"conditions":[{"type":"Ready","status":"Unknown","lastTransitionTime":"2020-01-01T00:00:00Z"}]}}'

sleep 2
pecho "5. Waiting for the controller to enforce the UnreadyPolicy and remove the dead node..."
echo "Waiting for node deletion..."
for i in {1..15}; do
  if ! kubectl get node $NODE_NAME >/dev/null 2>&1; then
    echo -e "${YELLOW}SUCCESS: Node $NODE_NAME was removed by the NodePool controller!${NC}"
    break
  fi
  sleep 1
done

sleep 1
pecho "6. Cleaning up NodePool..."
kubectl delete nodepool gpu-pool

sleep 1
pecho "Demo complete!"
# Restart kubelet just in case someone wants to reuse the cluster
docker exec $NODE_NAME systemctl start kubelet >/dev/null 2>&1 || true
