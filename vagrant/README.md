# Vagrant Kubernetes Cluster for Live Pod Migration Testing

This directory contains a simplified Vagrant setup for testing the live pod migration controller with a 2-node Kubernetes cluster.

## Prerequisites

- [Vagrant](https://www.vagrantup.com/downloads) installed
- [VirtualBox](https://www.virtualbox.org/wiki/Downloads) or [Parallels](https://www.parallels.com/) installed
- At least 6GB RAM available for VMs

## Cluster Architecture

- **Master Node** (`k8s-master`): 4GB RAM, 2 CPUs, IP: 192.168.56.10
- **Worker Node** (`k8s-worker`): 2GB RAM, 2 CPUs, IP: 192.168.56.11

## Quick Start

### 1. Start the Cluster

```bash
# From the vagrant directory
cd vagrant/

# Start both nodes (master first, then worker)
vagrant up master
vagrant up worker

# Or start both at once
vagrant up
```

### 2. Verify Cluster

```bash
# SSH into master node
vagrant ssh master

# Check cluster status
kubectl get nodes
kubectl get pods --all-namespaces

# Verify test deployment
kubectl get deployment nginx
kubectl get service nginx
```

### 3. Deploy Live Pod Migration Controller

```bash
# From inside the master node
cd /home/vagrant/live-pod-migration-controller

# Build and deploy the controller
make docker-build IMG=controller:latest
make docker-build-agent AGENT_IMG=checkpoint-agent:latest
make deploy IMG=controller:latest

# Verify deployment
kubectl get pods -n live-pod-migration-controller-system
```

### 4. Test Container Checkpoint

```bash
# Create a test pod
kubectl apply -f config/samples/test-pod.yaml

# Wait for pod to be ready
kubectl wait --for=condition=Ready pod/test-pod --timeout=60s

# Create a container checkpoint
kubectl apply -f - <<EOF
apiVersion: lpm.my.domain/v1
kind: ContainerCheckpoint
metadata:
  name: test-checkpoint
  namespace: default
spec:
  podName: test-pod
  containerName: nginx
EOF

# Watch the checkpoint progress
kubectl get containercheckpoint test-checkpoint -w
```

## What's Included

### Software Stack
- **Ubuntu 22.04** base image
- **Docker** container runtime
- **Kubernetes 1.28** (kubeadm, kubelet, kubectl)
- **Flannel CNI** for pod networking
- **Helm** package manager
- **Go 1.21.5** for building controllers

### Network Configuration
- Pod CIDR: `10.244.0.0/16`
- Service CIDR: `10.96.0.0/12` (default)
- Node network: `192.168.56.0/24`

## Useful Commands

```bash
# Cluster management
vagrant status                    # Check VM status
vagrant ssh master               # SSH to master
vagrant ssh worker               # SSH to worker
vagrant halt                     # Stop all VMs
vagrant destroy                  # Delete all VMs

# Kubernetes operations (from master)
kubectl get nodes -o wide        # Show node details
kubectl get pods --all-namespaces # Show all pods
kubectl describe node k8s-worker # Node details
kubectl top nodes                # Resource usage

# Controller debugging
kubectl logs -n live-pod-migration-controller-system deployment/live-pod-migration-controller-controller-manager
kubectl get pods -n live-pod-migration-controller-system -l app=checkpoint-agent
```

## Troubleshooting

### VMs Won't Start
- Ensure you have enough RAM (6GB minimum)
- Check VirtualBox/Parallels is properly installed
- Try `vagrant reload` if VMs are stuck

### Kubernetes Issues
- Check kubelet logs: `sudo journalctl -u kubelet -f`
- Verify Docker is running: `sudo systemctl status docker`
- Check CNI pods: `kubectl get pods -n kube-flannel`

### Controller Issues
- Verify CRDs are installed: `kubectl get crd | grep lpm.my.domain`
- Check controller logs for errors
- Ensure agent DaemonSet is running on both nodes

## Development Workflow

1. **Make changes** to the controller code on your host machine
2. **Sync changes** to VMs: `vagrant rsync`
3. **Rebuild** inside master: `make docker-build IMG=controller:latest`
4. **Redeploy**: `kubectl rollout restart deployment/live-pod-migration-controller-controller-manager -n live-pod-migration-controller-system`
5. **Test** your changes with sample workloads

## Cleanup

```bash
# Stop VMs
vagrant halt

# Remove VMs completely
vagrant destroy -f

# Clean up any leftover files
rm -f join-command.sh
```

This setup provides a clean, minimal Kubernetes environment perfect for testing the live pod migration system without the complexity of custom builds.
