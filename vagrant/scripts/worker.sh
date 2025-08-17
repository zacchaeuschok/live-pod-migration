#!/bin/bash

set -euxo pipefail

# Note: common.sh is run separately by Vagrant, no need to source it here

# Wait for join command to be available
while [ ! -f /tmp_sync/setup.sh ]; do
  echo "Waiting for join command..."
  sleep 5
done

# Join the cluster
sudo bash /tmp_sync/setup.sh

# Remove the script
sudo rm /tmp_sync/setup.sh

echo "Worker node setup completed successfully!"
