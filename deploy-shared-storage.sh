#!/bin/bash
set -euo pipefail

echo "Deploying shared storage for checkpoint migration..."

# Function to update NFS provisioner config with current IP
update_provisioner_config() {
    local master_ip=$(ip -4 addr show eth1 | grep -oP '(?<=inet\s)\d+(\.\d+){3}')
    echo "Updating NFS provisioner config with master IP: $master_ip"
    
    # Update the provisioner config with actual IP
    sed -i "s/10\.211\.55\.175/$master_ip/g" config/storage/nfs-provisioner.yaml
}

echo "1. Updating NFS provisioner configuration..."
update_provisioner_config

echo "2. Deploying NFS provisioner..."
kubectl apply -f config/storage/nfs-provisioner.yaml

echo "3. Waiting for NFS provisioner to be ready..."
kubectl wait --for=condition=available deployment/nfs-subdir-external-provisioner -n kube-system --timeout=120s

echo "4. Creating checkpoint PVC..."
kubectl apply -f config/storage/checkpoint-pvc.yaml

echo "5. Waiting for PVC to be bound..."
kubectl wait --for=condition=bound pvc/checkpoint-repo -n live-pod-migration-controller-system --timeout=60s

echo ""
echo "✅ Shared storage deployment complete!"
echo ""
echo "Verification:"
echo "kubectl get pvc -n live-pod-migration-controller-system"
echo "kubectl get pods -n kube-system -l app=nfs-subdir-external-provisioner"