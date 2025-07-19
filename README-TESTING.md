# Live Pod Migration Controller - Testing Guide

## Overview

This system implements a complete control-plane and node agent architecture for live pod migration using checkpoint/restore operations.

## Architecture

- **Control-Plane Operator**: Runs PodMigration, PodCheckpoint, ContainerCheckpoint reconcilers
- **Node Checkpoint Agent**: Privileged DaemonSet that performs checkpoint/restore operations via gRPC

## Quick Start

### 1. Build and Deploy

```bash
# Clean up old deployment first
make undeploy || true

# Clean up old images to ensure fresh builds
sudo crictl rmi localhost/controller:latest localhost/checkpoint-agent:latest || true

# Remove dangling images (skip if in use)
sudo crictl images | grep '<none>' | awk '{print $3}' | while read img; do sudo crictl rmi "$img" 2>/dev/null || echo "Skipping image $img (in use)"; done

# Build the controller manager with buildah
sudo buildah bud -t localhost/controller:latest .

# Build the checkpoint agent with buildah
sudo buildah bud -t localhost/checkpoint-agent:latest -f Dockerfile.agent .

# Push directly into CRI-O's local store
sudo buildah push localhost/controller:latest oci:/var/lib/containers/storage:localhost/controller:latest
sudo buildah push localhost/checkpoint-agent:latest oci:/var/lib/containers/storage:localhost/checkpoint-agent:latest

# Deploy the system (includes CRDs, RBAC, controller, and agent DaemonSet)
make deploy IMG=localhost/controller:latest AGENT_IMG=localhost/checkpoint-agent:latest

# Deploy shared storage for cross-node checkpoint access
./deploy-shared-storage.sh
```

### 2. Verify Deployment

```bash
# Check that the pods are running
kubectl get pods -n live-pod-migration-controller-system

# Verify images are built and available
sudo crictl images | grep localhost

# Check agent logs
kubectl logs -n live-pod-migration-controller-system -l app=checkpoint-agent

# Check controller logs (note the prefix is "lpm-" not "live-pod-migration-controller-")
kubectl logs -n live-pod-migration-controller-system deployment/lpm-controller-manager
```

### 3. Troubleshooting

#### CNI Network Issues

If the controller pod is stuck in ContainerCreating with CNI errors:

```bash
# Stop kubelet temporarily
sudo systemctl stop kubelet

# Remove the CNI bridge interface
sudo ip link delete cni0
sudo ip link delete flannel.1

# Restart kubelet
sudo systemctl start kubelet

# Delete the stuck pod to force recreation
kubectl delete pod -n live-pod-migration-controller-system -l control-plane=controller-manager
```

### 4. Test Container Checkpoint

```bash
# Create a test pod
kubectl apply -f config/samples/test-pod.yaml

# Wait for pod to be running
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

### 5. Test Pod Checkpoint (Multiple Containers)

```bash
# Create a multi-container test pod
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: multi-container-pod
  namespace: default
spec:
  containers:
  - name: nginx
    image: docker.io/library/nginx:1.21
    ports:
    - containerPort: 80
  - name: busybox
    image: docker.io/library/busybox:1.35
    command: ['sh', '-c', 'while true; do echo "Hello World"; sleep 5; done']
EOF

# Wait for pod to be running
kubectl wait --for=condition=Ready pod/multi-container-pod --timeout=60s

# Create a pod checkpoint (checkpoints all containers)
kubectl apply -f - <<EOF
apiVersion: lpm.my.domain/v1
kind: PodCheckpoint
metadata:
  name: multi-container-checkpoint
  namespace: default
spec:
  podName: multi-container-pod
EOF

# Watch the checkpoint progress
kubectl get podcheckpoint multi-container-checkpoint -w

# Verify individual container checkpoints were created
kubectl get containercheckpoint

# Check the final status
kubectl describe podcheckpoint multi-container-checkpoint

# Verify pod checkpoint content was created
kubectl get podcheckpointcontent
```

### 6. Verify Shared Storage

```bash
# Check shared storage is working
kubectl get pvc -n live-pod-migration-controller-system

# Verify NFS provisioner is running
kubectl get pods -n kube-system -l app=nfs-subdir-external-provisioner

# Check agent pods have shared storage mounted
kubectl get pods -n live-pod-migration-controller-system -l app=checkpoint-agent -o jsonpath='{.items[0].spec.volumes[?(@.name=="checkpoint-repo")].persistentVolumeClaim.claimName}'

# Test checkpoint files are saved to shared storage
kubectl exec -n live-pod-migration-controller-system $(kubectl get pods -n live-pod-migration-controller-system -l app=checkpoint-agent -o jsonpath='{.items[0].metadata.name}') -- ls -la /mnt/checkpoints/
```

### 7. Test Live Pod Migration

```bash
# Create a test pod with some state - scheduled on master node
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: stateful-pod
  namespace: default
spec:
  nodeSelector:
    kubernetes.io/hostname: k8s-master
  containers:
  - name: nginx
    image: docker.io/library/nginx:1.21
    ports:
    - containerPort: 80
    volumeMounts:
    - name: data
      mountPath: /data
  - name: writer
    image: docker.io/library/busybox:1.35
    command: ['sh', '-c', 'while true; do echo "$(date): Hello from $(hostname)" >> /data/log.txt; sleep 2; done']
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    emptyDir: {}
EOF

# Wait for pod to be running and accumulate some state
kubectl wait --for=condition=Ready pod/stateful-pod --timeout=60s
sleep 10

# Verify pod is running on master node
kubectl get pod stateful-pod -o wide

# Check the data being written to verify state
kubectl exec stateful-pod -c writer -- tail -5 /data/log.txt

# Create a PodMigration to migrate from master to worker
kubectl apply -f - <<EOF
apiVersion: lpm.my.domain/v1
kind: PodMigration
metadata:
  name: migrate-master-to-worker
  namespace: default
spec:
  podName: stateful-pod
  targetNode: k8s-worker
EOF

# Watch the migration progress
kubectl get podmigration migrate-master-to-worker -w

# Verify checkpoint was created in shared storage
kubectl get podcheckpoint
kubectl get containercheckpoint

# Check checkpoint files are in shared storage (accessible from any node)
kubectl exec -n live-pod-migration-controller-system $(kubectl get pods -n live-pod-migration-controller-system -l app=checkpoint-agent -o jsonpath='{.items[0].metadata.name}') -- ls -la /mnt/checkpoints/

# Verify the checkpoint content URIs use shared:// prefix
kubectl get containercheckpointcontent -o jsonpath='{.items[*].spec.artifactURI}' | tr ' ' '\n' | grep shared://

# After migration completes, verify pod moved to worker node
kubectl get pod stateful-pod -o wide

# Verify state was preserved by checking the log file
kubectl exec stateful-pod -c writer -- tail -10 /data/log.txt

# Check that timestamps show continuity (no major gaps indicating successful state preservation)
```

### 8. Verify Cross-Node Checkpoint Access

```bash
# Check which nodes have checkpoint agents
kubectl get pods -n live-pod-migration-controller-system -l app=checkpoint-agent -o wide

# Verify shared storage is accessible from all nodes
for node in $(kubectl get nodes -o jsonpath='{.items[*].metadata.name}'); do
  echo "=== Node: $node ==="
  POD=$(kubectl get pods -n live-pod-migration-controller-system -l app=checkpoint-agent --field-selector spec.nodeName=$node -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
  if [ -n "$POD" ]; then
    kubectl exec -n live-pod-migration-controller-system $POD -- ls -la /mnt/checkpoints/
  else
    echo "No agent on $node"
  fi
done

# Test checkpoint file accessibility across nodes
POD=$(kubectl get pods -n live-pod-migration-controller-system -l app=checkpoint-agent -o jsonpath='{.items[0].metadata.name}')
echo "Testing checkpoint file access across nodes:"
kubectl exec -n live-pod-migration-controller-system $POD -- ls -la /mnt/checkpoints/
```

### 9. Verify Agent Operation

```bash
# Check agent pods are running
kubectl get pods -n live-pod-migration-controller-system -l app=checkpoint-agent

# Check agent logs
kubectl logs -n live-pod-migration-controller-system -l app=checkpoint-agent

# Check controller logs
kubectl logs -n live-pod-migration-controller-system deployment/lpm-controller-manager

# Check for checkpoint operations in logs
kubectl logs -n live-pod-migration-controller-system -l app=checkpoint-agent | grep -i checkpoint
```

## Expected Behavior

### ContainerCheckpoint Workflow
1. **ContainerCheckpoint** transitions through phases:
   - `Pending` → validates pod and container exist
   - `Running` → calls agent to perform checkpoint (only once, no duplicates)
   - `Succeeded` → checkpoint artifact created and bound to content

2. **Agent** creates real checkpoint files at `/var/lib/kubelet/checkpoints/checkpoint-<pod>_<namespace>-<container>-<timestamp>.tar`

3. **Shared Storage**: Checkpoint files are automatically copied to shared NFS storage at `/mnt/checkpoints/<podUID>-<container>-<timestamp>.tar`

4. **ContainerCheckpointContent** is automatically created with artifact URI using `shared://` prefix for cross-node access

### PodCheckpoint Workflow  
1. **PodCheckpoint** transitions through phases:
   - `Pending` → validates pod exists, creates ContainerCheckpoint for each container
   - `Running` → waits for all container checkpoints to complete
   - `Succeeded` → creates PodCheckpointContent aggregating all container contents

2. **Hierarchical Creation**:
   ```
   PodCheckpoint → ContainerCheckpoint (per container) → ContainerCheckpointContent
                                     ↓
                   PodCheckpointContent (aggregates all container contents)
   ```

3. **Resource Naming**: Child resources use deterministic names like `<podcheckpoint-name>-<container-name>`

### PodMigration Workflow
1. **PodMigration** orchestrates the complete migration process:
   - `Pending` → validates source pod exists and is running
   - `CheckpointInProgress` → creates PodCheckpoint for the source pod
   - `CheckpointCompleted` → waits for checkpoint to complete successfully
   - `MigrationInProgress` → schedules pod on destination node (if specified)
   - `RestoreInProgress` → restores pod from checkpoint on destination node
   - `Succeeded` → migration completed, source pod terminated

2. **Cross-Node Capability**: Checkpoint files stored in shared storage enable migration between any nodes in the cluster

3. **Automatic Scheduling**: If no destination node specified, scheduler selects optimal target node

### Shared Storage Behavior
1. **NFS-based Storage**: Uses NFS subdir external provisioner for ReadWriteMany access
2. **Checkpoint Files**: Accessible from all nodes at `/mnt/checkpoints/` 
3. **Fallback Mechanism**: Falls back to local storage if shared storage unavailable
4. **URI Format**: Shared files use `shared://<filename>` format, local files use `file://<path>` format

## Troubleshooting

### Agent Not Responding
- Check if agent pods are running: `kubectl get pods -n live-pod-migration-controller-system`
- Verify hostPort 50051 is available on nodes
- Check agent logs for gRPC server startup

### Checkpoint Stuck in Pending
- Verify pod exists and is running
- Check container name matches exactly
- Review controller logs for validation errors

### Permission Issues
- Ensure agent has privileged security context
- Verify RBAC permissions for controller and agent
- Check if nodes allow privileged containers

### Certificate Issues

If the checkpoint agent fails with certificate errors like "no such file or directory":

```bash
# 1. Find where kubelet certificates are located
sudo find /etc/kubernetes -name "*.crt" -o -name "*.key" | grep -E "(kubelet|client)"

# 2. Check kubelet config for certificate paths
sudo cat /var/lib/kubelet/config.yaml | grep -E "(client|cert)"

# 3. Check kubelet process arguments
ps aux | grep kubelet | grep -E "(client-cert|client-key)"

# 4. List what's in the standard pki directory
ls -la /etc/kubernetes/pki/

# 5. Check for kubelet certificates in alternative locations
sudo find /var/lib/kubelet -name "*.crt" -o -name "*.key"
```

Common certificate locations:
- `/etc/kubernetes/pki/apiserver-kubelet-client.crt/key` (kubeadm default)
- `/var/lib/kubelet/pki/kubelet-client-current.pem` (some clusters)
- `/etc/ssl/certs/kubelet/` (alternative setups)

If certificates are in different locations, update the paths in `cmd/checkpoint-agent/main.go` constants.

### Kubelet Checkpoint API Issues

If the checkpoint agent gets "404 page not found" from kubelet:

```bash
# 1. Check if CRIU is installed
which criu || echo "CRIU not found"

# 2. Check kubelet version and features
kubelet --version

# 3. Check if kubelet has checkpoint feature enabled
ps aux | grep kubelet | grep -o -- '--feature-gates=[^[:space:]]*'

# 4. Test kubelet checkpoint API directly
curl -k --cert /etc/kubernetes/pki/apiserver-kubelet-client.crt \
     --key /etc/kubernetes/pki/apiserver-kubelet-client.key \
     https://localhost:10250/checkpoint/default/test-pod/nginx

# 5. Check kubelet config for checkpoint support
sudo cat /var/lib/kubelet/config.yaml | grep -i checkpoint
```

**Common issues:**
- Kubelet version < 1.25 (checkpoint API not available)
- CRIU not installed on the node
- Checkpoint feature gate not enabled: `--feature-gates=ContainerCheckpoint=true`
- Container runtime doesn't support checkpointing

**To enable checkpointing on kubeadm clusters:**

1. **Install CRIU:**
   ```bash
   sudo apt-get update
   sudo apt-get install -y criu runc
   ```

2. **Enable CRIU support in CRI-O:**
   ```bash
   sudo sed -i 's/^# enable_criu_support = false/enable_criu_support = true/' /etc/crio/crio.conf
   sudo systemctl restart crio
   ```

3. **Enable checkpoint feature gate in kubelet:**
   ```bash
   sudo sed -i 's|ExecStart=.*|ExecStart=/usr/bin/kubelet --config=/var/lib/kubelet/config.yaml --container-runtime-endpoint=unix:///var/run/crio/crio.sock --feature-gates=ContainerCheckpoint=true|' \
     /lib/systemd/system/kubelet.service
   
   sudo systemctl daemon-reload
   sudo systemctl restart kubelet
   ```

4. **Verify configuration:**
   ```bash
   # Check feature gate is enabled
   ps aux | grep kubelet | grep -o -- '--feature-gates=[^[:space:]]*'
   
   # Test checkpoint API endpoint
   curl -k --cert /etc/kubernetes/pki/apiserver-kubelet-client.crt \
        --key /etc/kubernetes/pki/apiserver-kubelet-client.key \
        https://localhost:10250/checkpoint/default/test-pod/nginx
   ```

**Note:** The systemd drop-in approach is more reliable than modifying `/var/lib/kubelet/config.yaml` since kubeadm may regenerate that file.

**For existing VMs that need checkpointing enabled:**

The key is to configure kubelet through kubeadm config, like the kubeadm-scripts do:

```bash
# 1. Fix CRI-O registry configuration  
sudo mkdir -p /etc/containers
sudo tee /etc/containers/registries.conf <<EOF
unqualified-search-registries = ["docker.io", "quay.io", "gcr.io", "registry.k8s.io"]
EOF
sudo systemctl restart crio

# 2. Enable CRIU support in CRI-O
sudo sed -i 's/^# enable_criu_support = false/enable_criu_support = true/' /etc/crio/crio.conf
sudo systemctl restart crio

# 3. Install CRIU if not present
sudo apt-get update && sudo apt-get install -y criu runc

# 4. Enable ContainerCheckpoint feature gate in kubelet config
sudo cp /var/lib/kubelet/config.yaml /var/lib/kubelet/config.yaml.backup
sudo tee -a /var/lib/kubelet/config.yaml <<EOF
featureGates:
  ContainerCheckpoint: true
EOF

sudo systemctl restart kubelet

# 5. Verify feature gate is applied (multiple methods)
# Method 1: Check kubelet process arguments
ps aux | grep kubelet | grep -o -- '--feature-gates=[^[:space:]]*'

# Method 2: Check via metrics endpoint (Kubernetes 1.26+)  
kubectl get --raw /metrics | grep kubernetes_feature_enabled | grep ContainerCheckpoint

# Method 3: Check kubelet config file
sudo cat /var/lib/kubelet/config.yaml | grep -A5 featureGates

# 6. Fix certificate permissions and test checkpoint API
sudo chmod 644 /etc/kubernetes/pki/apiserver-kubelet-client.crt
sudo chmod 600 /etc/kubernetes/pki/apiserver-kubelet-client.key

# Create test pod
kubectl apply -f config/samples/test-pod.yaml
kubectl wait --for=condition=Ready pod/test-pod --timeout=60s

# Test checkpoint API with proper certificates (use POST method)
sudo curl -X POST -k --cert /etc/kubernetes/pki/apiserver-kubelet-client.crt \
     --key /etc/kubernetes/pki/apiserver-kubelet-client.key \
     https://localhost:10250/checkpoint/default/test-pod/nginx

# If successful, the checkpoint agent should now work!
# Test the full controller system
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

## Cleanup and Reset

If you need to start fresh or encounter issues with your deployment, use these commands to clean up:

```bash
# 1. Delete all custom resources first (before removing CRDs)
kubectl delete containercheckpoint --all --all-namespaces --ignore-not-found=true
kubectl delete podcheckpoint --all --all-namespaces --ignore-not-found=true
kubectl delete containercheckpointcontent --all --all-namespaces --ignore-not-found=true
kubectl delete podcheckpointcontent --all --all-namespaces --ignore-not-found=true

# 2. Delete all sample resources
kubectl delete -f config/samples/ --ignore-not-found=true

# 3. Delete controller deployment and agents
kubectl delete -n live-pod-migration-controller-system deployment lpm-controller-manager --ignore-not-found=true
kubectl delete -n live-pod-migration-controller-system daemonset lpm-live-pod-migration-controller-checkpoint-agent --ignore-not-found=true

# Delete any old daemonset that might be causing port conflicts
kubectl delete daemonset -n live-pod-migration-controller-system live-pod-migration-controller-live-pod-migration-controller-checkpoint-agent --ignore-not-found=true

# 4. Delete the namespace (this will delete all remaining resources)
kubectl delete namespace live-pod-migration-controller-system --ignore-not-found=true

# 5. Delete CRDs (after all custom resources are gone)
kubectl delete crd containercheckpoints.lpm.my.domain --ignore-not-found=true
kubectl delete crd podcheckpoints.lpm.my.domain --ignore-not-found=true
kubectl delete crd containercheckpointcontents.lpm.my.domain --ignore-not-found=true
kubectl delete crd podcheckpointcontents.lpm.my.domain --ignore-not-found=true
kubectl delete crd podmigrations.lpm.my.domain --ignore-not-found=true

# Alternative: Delete all CRDs from files
kubectl delete -f config/crd/bases/ --ignore-not-found=true

# 6. Complete reset using make (removes all deployed components)
make undeploy

# 7. Remove container images from CRI-O storage
sudo crictl rmi localhost/checkpoint-agent:latest localhost/controller:latest || true

# 8. Clean up checkpoint files from kubelet directory
sudo find /var/lib/kubelet/checkpoints/ -name "checkpoint-*.tar" -delete

# 9. Clean up shared storage infrastructure
kubectl delete -f config/storage/checkpoint-pvc.yaml --ignore-not-found=true
kubectl delete -f config/storage/nfs-provisioner.yaml --ignore-not-found=true
kubectl delete job/nfs-setup -n kube-system --ignore-not-found=true
kubectl delete configmap/nfs-setup-script -n kube-system --ignore-not-found=true

# 10. Optional: Clean up test pods and migrations
kubectl delete pod test-pod multi-container-pod stateful-pod --ignore-not-found=true
kubectl delete podmigration --all --all-namespaces --ignore-not-found=true
```

After cleanup, you can follow the build and deploy steps again to start fresh.

## Development

### Local Testing
```bash
# Run controller locally (requires kubeconfig)
make run

# Build agent binary
go build -o bin/checkpoint-agent cmd/checkpoint-agent/main.go

# Test agent locally (requires proper setup)
./bin/checkpoint-agent
```

### Extending for Real Checkpoints
Replace the fake checkpoint implementation in `cmd/checkpoint-agent/main.go` with:
- CRIU integration for process checkpointing
- Container runtime API calls
- Proper filesystem snapshot handling
- Network state preservation

## Architecture Details

The system follows a clean separation of concerns:
- **Control-plane**: Manages CRD lifecycle and status
- **Node agents**: Perform privileged operations via simple gRPC interface
- **Discovery**: Agents found via hostPort or headless service
- **Error handling**: Explicit failure modes with clear messages
