#!/bin/bash
set -euo pipefail

echo "Deploying shared storage for checkpoint migration..."

# Function to setup NFS manually
setup_nfs_manually() {
    echo "Setting up NFS server manually..."
    
    # Install NFS server
    sudo apt-get update -qq
    sudo apt-get install -y nfs-kernel-server nfs-common
    
    # Create shared directory
    sudo mkdir -p /var/nfs/checkpoint-storage
    sudo chown nobody:nogroup /var/nfs/checkpoint-storage
    sudo chmod 777 /var/nfs/checkpoint-storage
    
    # Configure NFS exports (replace file to avoid duplicates)
    echo "/var/nfs/checkpoint-storage *(rw,sync,no_subtree_check,no_root_squash)" | sudo tee /etc/exports
    
    # Restart NFS services
    sudo systemctl restart nfs-kernel-server
    sudo systemctl enable nfs-kernel-server
    
    # Verify NFS is working
    sudo exportfs -ra
    sudo exportfs -v
    
    echo "NFS server setup complete!"
}

# Function to update NFS provisioner config with current IP
update_provisioner_config() {
    local master_ip=$(hostname -I | awk '{print $1}')
    echo "Updating NFS provisioner config with master IP: $master_ip"
    
    # Update the provisioner config with actual IP
    sed -i "s/10\.211\.55\.175/$master_ip/g" config/storage/nfs-provisioner.yaml
}


# Function to install NFS client on worker nodes
install_nfs_client_on_workers() {
    echo "Installing NFS client on worker nodes..."
    
    for worker in $(kubectl get nodes --no-headers | grep -v master | awk '{print $1}'); do
        echo "Installing NFS client on $worker..."
        ssh -o StrictHostKeyChecking=no vagrant@$worker "sudo apt-get update -qq && sudo apt-get install -y nfs-common || echo 'NFS client already installed or dependency issue - continuing'"
    done
    echo "NFS client installation complete!"
}

# Main deployment steps
echo "1. Setting up NFS server..."
setup_nfs_manually

echo "2. Installing NFS client on worker nodes..."
install_nfs_client_on_workers

echo "3. Updating NFS provisioner configuration..."
update_provisioner_config

echo "4. Deploying NFS provisioner..."
kubectl apply -f config/storage/nfs-provisioner.yaml

echo "5. Waiting for NFS provisioner to be ready..."
kubectl wait --for=condition=available deployment/nfs-subdir-external-provisioner -n kube-system --timeout=120s

echo "6. Creating checkpoint PVC..."
kubectl apply -f config/storage/checkpoint-pvc.yaml

echo "7. Waiting for PVC to be bound..."
kubectl wait --for=condition=bound pvc/checkpoint-repo -n live-pod-migration-controller-system --timeout=60s

echo ""
echo "✅ Shared storage deployment complete!"
echo ""
echo "Verification:"
echo "kubectl get pvc -n live-pod-migration-controller-system"
echo "kubectl get pods -n kube-system -l app=nfs-subdir-external-provisioner"