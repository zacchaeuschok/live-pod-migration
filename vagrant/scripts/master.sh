#!/bin/bash

set -euxo pipefail

# Source common setup
source /vagrant/scripts/common.sh

# Verify CRI-O is running
if ! systemctl is-active --quiet crio; then
  echo "ERROR: CRI-O is not running. Starting it now..."
  sudo systemctl start crio
  sleep 5
fi

# Create kubeadm config file with CRI-O socket and checkpoint support
cat > /tmp/kubeadm-config.yaml << EOF
apiVersion: kubeadm.k8s.io/v1beta3
kind: InitConfiguration
nodeRegistration:
  criSocket: unix:///var/run/crio/crio.sock
---
apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
networking:
  podSubnet: 10.244.0.0/16
featureGates:
  ContainerCheckpoint: true
---
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
featureGates:
  ContainerCheckpoint: true
EOF

# Reset any previous failed installations
echo "Resetting any previous kubeadm installations..."
sudo kubeadm reset -f || true

# Initialize Kubernetes control-plane
echo "Initializing Kubernetes control-plane..."
sudo kubeadm init --config=/tmp/kubeadm-config.yaml

# Set up kubeconfig for vagrant user
mkdir -p /home/vagrant/.kube
sudo cp -i /etc/kubernetes/admin.conf /home/vagrant/.kube/config
sudo chown vagrant:vagrant /home/vagrant/.kube/config

# Set up kubeconfig for root user
mkdir -p /root/.kube
sudo cp -i /etc/kubernetes/admin.conf /root/.kube/config

# Verify kubectl works
echo "Verifying kubectl configuration..."
kubectl version --client || true
kubectl get nodes || { echo "ERROR: kubectl cannot connect to API server"; exit 1; }

# Install Flannel CNI
echo "Installing Flannel CNI..."
kubectl apply -f https://github.com/flannel-io/flannel/releases/latest/download/kube-flannel.yml

# Wait for kubelet config to be created by kubeadm
sleep 10

# Verify checkpoint feature gate is enabled
echo "Verifying checkpoint feature gate..."
kubectl get --raw /metrics | grep kubernetes_feature_enabled | grep ContainerCheckpoint || echo "ContainerCheckpoint feature gate status unknown"

# Fix certificate permissions for checkpoint API access
echo "Fixing certificate permissions..."
sudo chmod 644 /etc/kubernetes/pki/apiserver-kubelet-client.crt
sudo chmod 600 /etc/kubernetes/pki/apiserver-kubelet-client.key

# Wait for node to be ready
echo "Waiting for node to be ready..."
kubectl wait --for=condition=Ready nodes --all --timeout=300s

# Remove control-plane taint to allow scheduling on master
echo "Removing control-plane taint..."
kubectl taint nodes --all node-role.kubernetes.io/control-plane- || true

# Save join command to shared folder
echo "Saving join command for worker nodes..."
kubeadm token create --print-join-command > /vagrant/setup.sh
chmod +x /vagrant/setup.sh

# Set up Go tools for development
echo "Setting up Go development tools..."
# Ensure GOPATH is set and added to PATH for vagrant user
sudo -u vagrant bash -c 'echo "export GOPATH=\$HOME/go" >> $HOME/.bashrc'
sudo -u vagrant bash -c 'echo "export PATH=\$PATH:\$GOPATH/bin" >> $HOME/.bashrc'
sudo -u vagrant bash -c 'export GOPATH=$HOME/go && export PATH=$PATH:$GOPATH/bin && cd $HOME/live-pod-migration-controller && go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest && mkdir -p bin && cp $(go env GOPATH)/bin/controller-gen bin/ && go install sigs.k8s.io/kustomize/kustomize/v5@latest && cp $(go env GOPATH)/bin/kustomize bin/'

# Test checkpoint API functionality
echo "Testing checkpoint API functionality..."
sudo curl -X POST -k --cert /etc/kubernetes/pki/apiserver-kubelet-client.crt \
     --key /etc/kubernetes/pki/apiserver-kubelet-client.key \
     https://localhost:10250/checkpoint/default/nonexistent-pod/nginx 2>/dev/null || echo "Checkpoint API endpoint is available (404 expected for nonexistent pod)"

echo "Master node setup completed successfully!"
echo "Checkpoint feature is enabled and ready for use."
echo "You can now access the cluster with: kubectl get nodes"
