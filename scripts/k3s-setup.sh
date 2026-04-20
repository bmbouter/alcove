#!/usr/bin/env bash
# k3s-setup.sh — Install and configure k3s for local Alcove development.
# Idempotent: safe to run multiple times.
set -euo pipefail

K3S_CONFIG_DIR="${HOME}/.kube"
K3S_KUBECONFIG="${K3S_CONFIG_DIR}/k3s-config"

echo "=== Alcove k3s Setup ==="

# --- Acquire sudo early so all subsequent sudo calls succeed ---
echo "This script requires sudo for k3s installation and configuration."
sudo -v || { echo "ERROR: sudo access is required. Run this in a terminal: ! make k3s-setup"; exit 1; }

# --- Prerequisite checks ---
echo "Checking prerequisites..."

if ! command -v curl &>/dev/null; then
    echo "ERROR: curl is required."
    exit 1
fi

if ! command -v podman &>/dev/null; then
    echo "ERROR: podman is required to build images."
    exit 1
fi

if ! command -v kubectl &>/dev/null; then
    echo "NOTE: kubectl not found. Will use 'sudo k3s kubectl' after install."
fi

# --- Install k3s if not present ---
if ! command -v k3s &>/dev/null; then
    echo "Installing k3s..."

    # Install k3s-selinux on Fedora/RHEL if SELinux is enforcing
    if command -v getenforce &>/dev/null && [ "$(getenforce)" = "Enforcing" ]; then
        echo "SELinux is enforcing. Installing k3s-selinux..."
        if command -v dnf &>/dev/null; then
            sudo dnf install -y k3s-selinux 2>/dev/null || echo "WARNING: k3s-selinux not found in repos; k3s may handle this."
        fi
    fi

    curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="server \
        --write-kubeconfig-mode=600 \
        --bind-address=127.0.0.1 \
        --disable=traefik \
        --disable=servicelb" \
        sh -
else
    echo "k3s already installed."
    if ! systemctl is-active --quiet k3s 2>/dev/null; then
        echo "Starting k3s service..."
        sudo systemctl start k3s
    fi
fi

# --- Wait for k3s node to be ready ---
echo "Waiting for k3s node..."
for i in $(seq 1 30); do
    if sudo k3s kubectl get nodes 2>/dev/null | grep -q ' Ready'; then
        echo "k3s node is Ready."
        break
    fi
    echo "  Waiting... ($i/30)"
    sleep 2
done

if ! sudo k3s kubectl get nodes 2>/dev/null | grep -q ' Ready'; then
    echo "ERROR: k3s node did not become ready. Check: sudo journalctl -u k3s"
    exit 1
fi

# --- Copy kubeconfig ---
mkdir -p "${K3S_CONFIG_DIR}"
sudo cp /etc/rancher/k3s/k3s.yaml "${K3S_KUBECONFIG}"
sudo chown "$(id -u):$(id -g)" "${K3S_KUBECONFIG}"
chmod 600 "${K3S_KUBECONFIG}"
echo "Kubeconfig written to ${K3S_KUBECONFIG}"

# --- Firewall rules for pod and service CIDRs ---
if command -v firewall-cmd &>/dev/null && systemctl is-active --quiet firewalld 2>/dev/null; then
    echo "Configuring firewalld for k3s traffic..."
    if ! sudo firewall-cmd --permanent --zone=trusted --list-sources 2>/dev/null | grep -q "10.42.0.0/16"; then
        sudo firewall-cmd --permanent --zone=trusted --add-source=10.42.0.0/16
    fi
    if ! sudo firewall-cmd --permanent --zone=trusted --list-sources 2>/dev/null | grep -q "10.43.0.0/16"; then
        sudo firewall-cmd --permanent --zone=trusted --add-source=10.43.0.0/16
    fi
    sudo firewall-cmd --reload
    echo "Firewalld configured (10.42.0.0/16 and 10.43.0.0/16 in trusted zone)."
fi

echo ""
echo "k3s setup complete."
echo "  KUBECONFIG=${K3S_KUBECONFIG}"
echo "  Run 'make k3s-up' to deploy Alcove infrastructure."
