#!/usr/bin/env bash
# k3s-bridge-endpoint.sh — Create Endpoints for alcove-bridge Service.
# Lets in-cluster pods reach Bridge running on the host.
set -euo pipefail

export KUBECONFIG="${1:-${HOME}/.kube/k3s-config}"

HOST_IP=$(kubectl get node -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')

if [ -z "${HOST_IP}" ]; then
    echo "ERROR: Could not detect host IP from k3s node."
    exit 1
fi

echo "Creating alcove-bridge endpoint -> ${HOST_IP}:8080"

kubectl apply -n alcove -f - <<EOF
apiVersion: v1
kind: Endpoints
metadata:
  name: alcove-bridge
  namespace: alcove
  labels:
    app.kubernetes.io/part-of: alcove
subsets:
  - addresses:
      - ip: "${HOST_IP}"
    ports:
      - port: 8080
        protocol: TCP
        name: http
EOF
