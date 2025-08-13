#!/bin/bash

set -euxo pipefail

# Note: common.sh is run separately by Vagrant, no need to source it here

# Wait for master to be ready and get join command
echo "Waiting for master node to be ready..."
MAX_ATTEMPTS=60
ATTEMPT=0

while [ $ATTEMPT -lt $MAX_ATTEMPTS ]; do
  # Try to get join command directly from master via SSH
  JOIN_CMD=$(ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR vagrant@192.168.56.10 "kubeadm token create --print-join-command 2>/dev/null" 2>/dev/null || true)
  
  if [ ! -z "$JOIN_CMD" ] && [[ "$JOIN_CMD" == *"kubeadm join"* ]]; then
    echo "Got join command from master, joining cluster..."
    sudo $JOIN_CMD
    break
  fi
  
  echo "Master not ready yet, attempt $ATTEMPT/$MAX_ATTEMPTS..."
  ATTEMPT=$((ATTEMPT + 1))
  sleep 10
done

if [ $ATTEMPT -eq $MAX_ATTEMPTS ]; then
  echo "ERROR: Could not get join command from master after $MAX_ATTEMPTS attempts"
  echo "You may need to join manually with: kubeadm token create --print-join-command (on master)"
  exit 1
fi

echo "Worker node setup completed successfully!"
