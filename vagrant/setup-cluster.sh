#!/bin/bash

# One-click cluster setup script
set -uo pipefail

echo "=== Starting Kubernetes Cluster Setup ==="

# Destroy any existing VMs
echo "Cleaning up any existing VMs..."
vagrant destroy -f

# Start master first
echo "Starting master node..."
vagrant up master

# Check if master provisioned successfully
if ! vagrant ssh master -c "kubectl get nodes" 2>/dev/null | grep -q "Ready"; then
    echo "ERROR: Master node failed to initialize properly"
    exit 1
fi

echo "Master node is ready!"

# Get join command from master
echo "Getting join command from master..."
JOIN_CMD=$(vagrant ssh master -c "kubeadm token create --print-join-command" 2>/dev/null)

if [ -z "$JOIN_CMD" ]; then
    echo "ERROR: Could not get join command from master"
    exit 1
fi

# Start worker
echo "Starting worker node..."
vagrant up worker

# Join worker to cluster
echo "Joining worker to cluster..."
vagrant ssh worker -c "sudo $JOIN_CMD"

# Verify cluster
echo "Verifying cluster status..."
sleep 10
vagrant ssh master -c "kubectl get nodes"

echo "=== Cluster Setup Complete ==="
echo "You can now access the cluster with: vagrant ssh master"
echo "Then run: kubectl get nodes"