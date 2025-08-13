#!/bin/bash

set -euxo pipefail

# Install controller-gen and other required Go tools
echo "Installing controller-gen and other required Go tools..."

# Ensure Go is in PATH
export PATH=$PATH:/usr/local/go/bin

# Ensure GOPATH is set and added to PATH
echo 'export GOPATH=$HOME/go' >> $HOME/.bashrc
echo 'export PATH=$PATH:$GOPATH/bin:/usr/local/go/bin' >> $HOME/.bashrc
export GOPATH=$HOME/go
export PATH=$PATH:$GOPATH/bin

# Install controller-gen with specific version for compatibility
go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.17.0

# Create bin directory in project if it doesn't exist
mkdir -p $HOME/live-pod-migration-controller/bin

# Remove any existing controller-gen binaries (they might be for wrong architecture)
rm -f $HOME/live-pod-migration-controller/bin/controller-gen*

# Copy controller-gen to project bin directory
cp $(go env GOPATH)/bin/controller-gen $HOME/live-pod-migration-controller/bin/

# Install kustomize if needed
go install sigs.k8s.io/kustomize/kustomize/v5@latest
cp $(go env GOPATH)/bin/kustomize $HOME/live-pod-migration-controller/bin/

echo "Go tools setup completed successfully!"
