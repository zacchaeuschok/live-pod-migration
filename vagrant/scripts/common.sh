#!/bin/bash

set -euxo pipefail

# Update system
sudo apt-get update -y
sudo apt-get upgrade -y

# Install prerequisites
sudo apt-get install -y apt-transport-https ca-certificates curl gnupg lsb-release

# Load required kernel modules
cat <<EOF | sudo tee /etc/modules-load.d/k8s.conf
overlay
br_netfilter
EOF

sudo modprobe overlay
sudo modprobe br_netfilter

# Set up required sysctl params
cat <<EOF | sudo tee /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF

sudo sysctl --system

# Install CRI-O directly from Ubuntu packages
sudo apt-get update -y
sudo apt-get install -y software-properties-common

# Add the CRI-O repository - using apt-key add as a workaround for expired keys
export OS=xUbuntu_22.04
export VERSION=1.28

# Create required directories for keyrings
sudo mkdir -p /etc/apt/keyrings

# Add the repositories with --force-yes to ignore expired keys
curl -fsSL https://download.opensuse.org/repositories/devel:/kubic:/libcontainers:/stable/$OS/Release.key | sudo gpg --batch --yes --dearmor -o /etc/apt/keyrings/libcontainers.gpg
curl -fsSL https://download.opensuse.org/repositories/devel:/kubic:/libcontainers:/stable:/cri-o:/$VERSION/$OS/Release.key | sudo gpg --batch --yes --dearmor -o /etc/apt/keyrings/crio.gpg

echo "deb [signed-by=/etc/apt/keyrings/libcontainers.gpg] https://download.opensuse.org/repositories/devel:/kubic:/libcontainers:/stable/$OS/ /" | sudo tee /etc/apt/sources.list.d/devel:kubic:libcontainers:stable.list
echo "deb [signed-by=/etc/apt/keyrings/crio.gpg] https://download.opensuse.org/repositories/devel:/kubic:/libcontainers:/stable:/cri-o:/$VERSION/$OS/ /" | sudo tee /etc/apt/sources.list.d/devel:kubic:libcontainers:stable:cri-o:$VERSION.list

# Update with --allow-unauthenticated to bypass key verification
sudo apt-get update -y --allow-unauthenticated

# Install CRI-O with --allow-unauthenticated to bypass key verification
sudo apt-get install -y --allow-unauthenticated cri-o cri-o-runc

# Configure CRI-O for checkpointing
sudo mkdir -p /etc/crio
sudo crio config | sudo tee /etc/crio/crio.conf
sudo sed -i 's/^# enable_criu_support = false/enable_criu_support = true/' /etc/crio/crio.conf

# Fix CRI-O registry configuration to avoid image resolution issues
sudo mkdir -p /etc/containers
sudo tee /etc/containers/registries.conf <<EOF
unqualified-search-registries = ["docker.io", "quay.io", "gcr.io", "registry.k8s.io"]
EOF

# Start and enable CRI-O
sudo systemctl daemon-reload
sudo systemctl enable crio
sudo systemctl start crio

# Install kubeadm, kubelet, kubectl
sudo apt-get update -y
sudo apt-get install -y apt-transport-https ca-certificates curl

# Fix for non-interactive GPG key installation
sudo mkdir -p /etc/apt/keyrings
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.28/deb/Release.key | sudo tee /etc/apt/keyrings/kubernetes.key > /dev/null
sudo gpg --batch --yes --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg /etc/apt/keyrings/kubernetes.key
sudo rm /etc/apt/keyrings/kubernetes.key

echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.28/deb/ /' | sudo tee /etc/apt/sources.list.d/kubernetes.list
sudo apt-get update -y
sudo apt-get install -y kubelet kubeadm kubectl
sudo apt-mark hold kubelet kubeadm kubectl

# Install buildah for CRI-O image management
echo "Installing buildah for CRI-O image management..."
sudo apt-get install -y buildah

# Configure kubelet to use CRI-O
sudo mkdir -p /etc/systemd/system/kubelet.service.d
cat <<EOF | sudo tee /etc/systemd/system/kubelet.service.d/10-crio.conf
[Service]
Environment="KUBELET_EXTRA_ARGS=--container-runtime-endpoint=unix:///var/run/crio/crio.sock"
EOF

sudo systemctl daemon-reload
sudo systemctl enable kubelet

# Install additional tools for development and checkpointing
sudo apt-get install -y git make wget curl tree jq criu runc nfs-common

# Install Go for building the controller
ARCH=$(dpkg --print-architecture)
if [ "$ARCH" = "amd64" ]; then
    GO_ARCH="amd64"
elif [ "$ARCH" = "arm64" ]; then
    GO_ARCH="arm64"
else
    echo "Unsupported architecture: $ARCH"
    exit 1
fi

wget -q https://go.dev/dl/go1.21.5.linux-${GO_ARCH}.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.21.5.linux-${GO_ARCH}.tar.gz
rm go1.21.5.linux-${GO_ARCH}.tar.gz

# Add Go to PATH
echo 'export PATH=$PATH:/usr/local/go/bin' >> /home/vagrant/.bashrc
echo 'export GOPATH=/home/vagrant/go' >> /home/vagrant/.bashrc
echo 'export PATH=$PATH:/home/vagrant/go/bin' >> /home/vagrant/.bashrc

# Disable swap (required for Kubernetes)
sudo swapoff -a
sudo sed -i '/ swap / s/^\(.*\)$/#\1/g' /etc/fstab

echo "Common setup completed successfully!"
