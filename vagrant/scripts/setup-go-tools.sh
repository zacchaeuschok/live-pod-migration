#!/bin/bash

set -euxo pipefail

# Install controller-gen and other required Go tools
echo "Installing controller-gen and other required Go tools..."

# Ensure GOPATH is set and added to PATH
echo 'export GOPATH=$HOME/go' >> $HOME/.bashrc
echo 'export PATH=$PATH:$GOPATH/bin' >> $HOME/.bashrc
export GOPATH=$HOME/go
export PATH=$PATH:$GOPATH/bin

# Install controller-gen
go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

# Create bin directory in project if it doesn't exist
mkdir -p $HOME/live-pod-migration-controller/bin

# Copy controller-gen to project bin directory
cp $(go env GOPATH)/bin/controller-gen $HOME/live-pod-migration-controller/bin/

# Install kustomize if needed
go install sigs.k8s.io/kustomize/kustomize/v5@latest
cp $(go env GOPATH)/bin/kustomize $HOME/live-pod-migration-controller/bin/

echo "Go tools setup completed successfully!"
