#!/bin/bash

# Use less strict error handling to avoid failing on warnings
set -uxo pipefail

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

# Install CRI-O 1.30 with Kubernetes 1.30 (newer versions may have working repos)
export VERSION=1.30

# Create required directories for keyrings
sudo mkdir -p /usr/share/keyrings

# Try using the new pkgs.k8s.io infrastructure for CRI-O 1.30
# First disable GPG verification temporarily to test if the repo works
echo "deb [trusted=yes] https://pkgs.k8s.io/addons:/cri-o:/stable:/v$VERSION/deb/ /" | sudo tee /etc/apt/sources.list.d/cri-o.list

# Update package lists
sudo apt-get update -y

# Install CRI-O
sudo apt-get install -y cri-o

# Configure CRI-O for checkpointing
sudo mkdir -p /etc/crio
sudo crio config | sudo tee /etc/crio/crio.conf
sudo sed -i 's/^# enable_criu_support = .*/enable_criu_support = true/' /etc/crio/crio.conf
sudo sed -i 's/default_runtime = "crun"/default_runtime = "runc"/' /etc/crio/crio.conf
sudo sed -i 's|runtime_path = "/usr/libexec/crio/runc"|runtime_path = "/usr/sbin/runc"|' /etc/crio/crio.conf

# Fix CRI-O registry configuration to avoid image resolution issues
sudo mkdir -p /etc/containers
sudo tee /etc/containers/registries.conf <<EOF
unqualified-search-registries = ["docker.io", "quay.io", "gcr.io", "registry.k8s.io"]
EOF

# Create containers policy.json for buildah
sudo tee /etc/containers/policy.json <<EOF
{
    "default": [
        {
            "type": "insecureAcceptAnything"
        }
    ],
    "transports":
        {
            "docker-daemon":
                {
                    "": [{"type":"insecureAcceptAnything"}]
                }
        }
}
EOF

# Start and enable CRI-O
sudo systemctl daemon-reload
sudo systemctl enable crio
sudo systemctl start crio

# Install kubeadm, kubelet, kubectl
sudo apt-get update -y
sudo apt-get install -y apt-transport-https ca-certificates curl

# Fix for non-interactive GPG key installation - update to Kubernetes 1.30
sudo mkdir -p /etc/apt/keyrings
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.30/deb/Release.key | sudo tee /etc/apt/keyrings/kubernetes.key > /dev/null
sudo gpg --batch --yes --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg /etc/apt/keyrings/kubernetes.key
sudo rm /etc/apt/keyrings/kubernetes.key

echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.30/deb/ /' | sudo tee /etc/apt/sources.list.d/kubernetes.list
sudo apt-get update -y
sudo apt-get install -y kubelet kubeadm kubectl
sudo apt-mark hold kubelet kubeadm kubectl

# Install buildah for CRI-O image management
echo "Installing buildah for CRI-O image management..."
# Use non-interactive mode to avoid config file prompts
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -o Dpkg::Options::="--force-confold" buildah

# Configure kubelet to use CRI-O
sudo mkdir -p /etc/systemd/system/kubelet.service.d
cat <<EOF | sudo tee /etc/systemd/system/kubelet.service.d/10-crio.conf
[Service]
Environment="KUBELET_EXTRA_ARGS=--container-runtime-endpoint=unix:///var/run/crio/crio.sock"
EOF

sudo systemctl daemon-reload
sudo systemctl enable kubelet

# Install additional tools for development and checkpointing
sudo apt-get install -y git make wget curl tree jq criu nfs-common build-essential libseccomp-dev pkg-config

# First install the Ubuntu packaged runc as a fallback
sudo apt-get install -y runc

# Install runc with CRIU support (Ubuntu package lacks CRIU support)
echo "Building runc with CRIU support..."

# Install newer Go version for building runc (bypass script error handling)
set +e
wget -q https://go.dev/dl/go1.22.2.linux-$(dpkg --print-architecture).tar.gz -O /tmp/go1.22.2.tar.gz
if [ $? -eq 0 ]; then
    sudo rm -rf /home/vagrant/.go
    sudo mkdir -p /home/vagrant/.go
    sudo tar -C /home/vagrant/.go -xzf /tmp/go1.22.2.tar.gz --strip-components=1
    sudo chown -R vagrant:vagrant /home/vagrant/.go
    rm /tmp/go1.22.2.tar.gz
    export PATH=/home/vagrant/.go/bin:$PATH
    
    # Clone and build runc with CRIU support
    cd /tmp
    git clone https://github.com/opencontainers/runc.git
    cd runc
    git checkout v1.2.5
    # Remove unsupported toolchain directive for older Go versions
    sed -i '/toolchain/d' go.mod
    # Build with CRIU support
    if /home/vagrant/.go/bin/go build -trimpath "-buildmode=pie" -tags "seccomp apparmor selinux criu" -ldflags "-X main.gitCommit=v1.2.5-criu -X main.version=1.2.5" -o runc .; then
        # Replace system runc with CRIU-enabled version
        sudo cp runc /usr/sbin/runc
        sudo chmod +x /usr/sbin/runc
        echo "runc with CRIU support installed successfully"
    else
        echo "WARNING: Failed to build runc with CRIU support, using system runc"
    fi
    
    # Clean up build directory
    cd /
    rm -rf /tmp/runc
else
    echo "WARNING: Failed to download Go for runc build, using system runc"
fi
set -e

# Fix CRI-O drop-in config to use runc instead of crun (CRI-O 1.30 defaults to crun)
if [ -f /etc/crio/crio.conf.d/10-crio.conf ]; then
    echo "Fixing CRI-O drop-in config to use runc..."
    sudo sed -i 's/default_runtime = "crun"/default_runtime = "runc"/' /etc/crio/crio.conf.d/10-crio.conf
    sudo sed -i 's|runtime_path = "/usr/libexec/crio/runc"|runtime_path = "/usr/sbin/runc"|' /etc/crio/crio.conf.d/10-crio.conf
fi

# Restart CRI-O to use the new runc
sudo systemctl restart crio
sleep 5

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
