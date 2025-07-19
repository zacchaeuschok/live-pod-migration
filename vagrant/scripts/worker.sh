#!/bin/bash

set -euxo pipefail

# Source common setup
source /vagrant/scripts/common.sh

# Wait for join command to be available
while [ ! -f /vagrant/setup.sh ]; do
  echo "Waiting for join command..."
  sleep 5
done

# Join the cluster
sudo bash /vagrant/setup.sh

echo "Worker node setup completed successfully!"
