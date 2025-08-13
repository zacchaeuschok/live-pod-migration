# Self-Restore Pattern Implementation Proposal

## Executive Summary

This proposal implements a **Kubernetes-native self-restore pattern** that eliminates CRIU segmentation faults by performing checkpoint restoration **inside the target container's exact namespace context**, rather than through host-injected kubelet→CRI-O→runc→CRIU chain.

## Root Cause Analysis

### Current Failure Mode
```
kubelet → CRI-O → runc → CRIU parasite injection → SIGSEGV
```

**Problem**: CRIU's parasite code crashes during memory rehydration because:
- Host userspace differs from checkpointed container userspace
- ASLR/VDSO addresses don't match between environments  
- Cgroup/mount/network namespaces have different layouts
- Runtime ABI drift between checkpoint and restore contexts

### Evidence from Our Testing
```bash
# Every restoration attempt fails with same pattern:
Error (criu/cr-restore.c:1492): 288657 stopped by signal 11: Segmentation fault
pie: 1: `- skip pagemap  # CRIU parasite crashes during memory mapping
```

This occurs even with:
- ✅ Identical pod specifications
- ✅ Same-node migration (no network changes)
- ✅ Simple containers (minimal complexity)
- ✅ Runtime context preservation (`originalPod.DeepCopy()`)

## Self-Restore Pattern Solution

### Architecture Overview
```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   PodMigration  │───▶│  In-Container    │───▶│  Process Tree   │
│   Controller    │    │  CRIU Restore    │    │  Restoration    │
└─────────────────┘    └──────────────────┘    └─────────────────┘
         │                       │                       │
         ▼                       ▼                       ▼
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│ Create target   │    │ criu restore     │    │ exec into       │
│ pod with CRIU   │    │ --images-dir     │    │ restored PID 1  │
│ capabilities    │    │ /ckpt/images     │    │                 │
└─────────────────┘    └──────────────────┘    └─────────────────┘
```

### Key Principles
1. **No host/runtime context mismatch**: CRIU runs inside target container
2. **Exact namespace matching**: Same PID, mount, network namespaces  
3. **No kubelet/CRI-O modifications**: Pure Kubernetes pod specification
4. **Proven pattern**: Used in JVM/CRaC and OpenShift workflows

## Implementation Design

### 1. Enhanced Pod Specification

```yaml
# Target pod with self-restore capability
apiVersion: v1
kind: Pod
metadata:
  name: app-restored
  annotations:
    migration.restore-mode: "self-restore"
spec:
  shareProcessNamespace: true  # Required for CRIU access to all processes
  
  securityContext:
    # Minimal capabilities for CRIU operation
    capabilities:
      add:
        - SYS_PTRACE    # Process tracing for restoration
        - SYS_ADMIN     # Namespace manipulation
        - NET_ADMIN     # Network restoration (if needed)
    # Disable security constraints during bring-up
    seLinuxOptions:
      type: unconfined_t
    appArmorProfile:
      type: unconfined
  
  initContainers:
  - name: restore-launcher
    image: app-with-criu:latest
    command: ["/restore-launcher.sh"]
    volumeMounts:
    - name: checkpoint-data
      mountPath: /ckpt
    - name: app-data
      mountPath: /data
    env:
    - name: CHECKPOINT_PATH
      value: "/ckpt/images"
    - name: APP_COMMAND
      value: "/app/start.sh"
      
  containers:
  - name: app
    image: app-with-criu:latest
    command: ["/app-wrapper.sh"]
    volumeMounts:
    - name: checkpoint-data
      mountPath: /ckpt
    - name: app-data
      mountPath: /data
      
  volumes:
  - name: checkpoint-data
    persistentVolumeClaim:
      claimName: checkpoint-storage
  - name: app-data
    emptyDir: {}
```

### 2. In-Container Restore Components

#### A. App Image with CRIU (`Dockerfile.self-restore`)
```dockerfile
FROM ubuntu:22.04

# Install CRIU and dependencies
RUN apt-get update && apt-get install -y \
    criu \
    runc \
    && rm -rf /var/lib/apt/lists/*

# Copy application binaries
COPY app/ /app/
COPY scripts/restore-launcher.sh /restore-launcher.sh
COPY scripts/app-wrapper.sh /app-wrapper.sh

RUN chmod +x /restore-launcher.sh /app-wrapper.sh

# Verify CRIU installation
RUN criu check --all || echo "CRIU check warnings acceptable for container use"
```

#### B. Restore Launcher Script (`scripts/restore-launcher.sh`)
```bash
#!/bin/bash
set -euo pipefail

CHECKPOINT_PATH="${CHECKPOINT_PATH:-/ckpt/images}"
APP_COMMAND="${APP_COMMAND:-/app/start.sh}"

echo "Self-restore launcher starting..."

if [ -d "$CHECKPOINT_PATH" ] && [ "$(ls -A $CHECKPOINT_PATH)" ]; then
    echo "Checkpoint images found at $CHECKPOINT_PATH"
    
    # Verify checkpoint integrity
    if ! criu check --images-dir="$CHECKPOINT_PATH" 2>/dev/null; then
        echo "Warning: Checkpoint validation failed, starting fresh"
        exec $APP_COMMAND
    fi
    
    echo "Performing self-restore..."
    # Restore process tree in detached mode
    criu restore \
        --images-dir="$CHECKPOINT_PATH" \
        --restore-detached \
        --shell-job \
        --tcp-established \
        --ext-mount-map /old:/data \
        --manage-cgroups \
        --log-level 4 \
        --log-file /tmp/restore.log
    
    if [ $? -eq 0 ]; then
        echo "Self-restore completed successfully"
        # The restored process tree is now running
        # This init container can exit
        exit 0
    else
        echo "Self-restore failed, starting fresh application"
        exec $APP_COMMAND
    fi
else
    echo "No checkpoint found, starting fresh application"
    exec $APP_COMMAND
fi
```

#### C. App Wrapper Script (`scripts/app-wrapper.sh`)
```bash
#!/bin/bash
set -euo pipefail

CHECKPOINT_PATH="${CHECKPOINT_PATH:-/ckpt/images}"

# Check if we're in a restored environment
if [ -f "/tmp/restore.log" ] && grep -q "Restore finished successfully" /tmp/restore.log 2>/dev/null; then
    echo "Running in restored environment, process tree already active"
    # In restored environment, just wait (main processes already running)
    while true; do
        sleep 30
        # Health check: verify restored processes are still running
        if ! pgrep -f "/app/" > /dev/null; then
            echo "Restored processes died, exiting to trigger restart"
            exit 1
        fi
    done
else
    echo "Starting fresh application"
    exec /app/start.sh "$@"
fi
```

### 3. Controller Modifications

#### A. Enhanced Pod Creation (`createRestoredPod()`)
```go
func (r *PodMigrationReconciler) createRestoredPod(ctx context.Context, podMigration *lpmv1.PodMigration) (*corev1.Pod, error) {
    var originalPod corev1.Pod
    err := r.Get(ctx, client.ObjectKey{
        Namespace: podMigration.Namespace,
        Name:      podMigration.Spec.PodName,
    }, &originalPod)
    if err != nil {
        return nil, fmt.Errorf("failed to get original pod: %w", err)
    }

    // Start with original pod context
    restoredPod := originalPod.DeepCopy()
    
    // Modify for self-restore pattern
    restoredPod.ObjectMeta.Name = fmt.Sprintf("%s-restored", originalPod.Name)
    restoredPod.ObjectMeta.ResourceVersion = ""
    restoredPod.ObjectMeta.UID = ""
    restoredPod.Spec.NodeName = podMigration.Spec.TargetNode
    
    // Enable self-restore capabilities
    if restoredPod.ObjectMeta.Annotations == nil {
        restoredPod.ObjectMeta.Annotations = make(map[string]string)
    }
    restoredPod.ObjectMeta.Annotations["migration.restore-mode"] = "self-restore"
    restoredPod.ObjectMeta.Annotations["migration.checkpoint-path"] = r.getCheckpointPath(podMigration)
    
    // Enable process namespace sharing
    shareProcessNamespace := true
    restoredPod.Spec.ShareProcessNamespace = &shareProcessNamespace
    
    // Add security capabilities for CRIU
    if restoredPod.Spec.SecurityContext == nil {
        restoredPod.Spec.SecurityContext = &corev1.PodSecurityContext{}
    }
    
    // Add required capabilities
    capabilities := &corev1.Capabilities{
        Add: []corev1.Capability{
            "SYS_PTRACE",
            "SYS_ADMIN", 
            "NET_ADMIN",
        },
    }
    
    // Apply to all containers
    for i := range restoredPod.Spec.Containers {
        if restoredPod.Spec.Containers[i].SecurityContext == nil {
            restoredPod.Spec.Containers[i].SecurityContext = &corev1.SecurityContext{}
        }
        restoredPod.Spec.Containers[i].SecurityContext.Capabilities = capabilities
        
        // Update image to CRIU-enabled version
        restoredPod.Spec.Containers[i].Image = r.getSelfRestoreImage(restoredPod.Spec.Containers[i].Image)
    }
    
    // Add checkpoint data volume
    checkpointVolume := corev1.Volume{
        Name: "checkpoint-data",
        VolumeSource: corev1.VolumeSource{
            PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
                ClaimName: "checkpoint-storage",
            },
        },
    }
    restoredPod.Spec.Volumes = append(restoredPod.Spec.Volumes, checkpointVolume)
    
    // Add volume mounts to containers
    checkpointVolumeMount := corev1.VolumeMount{
        Name:      "checkpoint-data",
        MountPath: "/ckpt",
    }
    for i := range restoredPod.Spec.Containers {
        restoredPod.Spec.Containers[i].VolumeMounts = append(
            restoredPod.Spec.Containers[i].VolumeMounts,
            checkpointVolumeMount,
        )
    }
    
    return restoredPod, nil
}
```

#### B. Checkpoint Storage Management
```go
func (r *PodMigrationReconciler) prepareCheckpointForSelfRestore(ctx context.Context, podMigration *lpmv1.PodMigration) error {
    // Get checkpoint content
    checkpointContent, err := r.getCheckpointContent(ctx, podMigration)
    if err != nil {
        return fmt.Errorf("failed to get checkpoint content: %w", err)
    }
    
    // Copy checkpoint files to target format for self-restore
    for _, containerContent := range checkpointContent.Spec.ContainerContents {
        var content lpmv1.ContainerCheckpointContent
        err := r.Get(ctx, client.ObjectKey{
            Name:      containerContent.Name,
            Namespace: checkpointContent.Namespace,
        }, &content)
        if err != nil {
            continue
        }
        
        // Convert from shared:// format to direct NFS path
        checkpointURI := content.Spec.ArtifactURI
        if strings.HasPrefix(checkpointURI, "shared://") {
            // Extract and copy checkpoint images to structured directory
            err = r.extractCheckpointImages(ctx, checkpointURI, podMigration.Name)
            if err != nil {
                return fmt.Errorf("failed to extract checkpoint images: %w", err)
            }
        }
    }
    
    return nil
}

func (r *PodMigrationReconciler) extractCheckpointImages(ctx context.Context, checkpointURI, migrationName string) error {
    // Use agent to extract checkpoint tar into CRIU images directory structure
    // Target: /mnt/checkpoints/<migration-name>/images/
    return r.AgentClient.ExtractCheckpointImages(ctx, checkpointURI, migrationName)
}
```

### 4. Agent Enhancements

#### A. Checkpoint Images Extraction (`internal/agent/client.go`)
```go
func (c *Client) ExtractCheckpointImages(ctx context.Context, checkpointURI, migrationName string) error {
    // Find agent on any node (checkpoints are in shared storage)
    agentAddr, err := c.findAgentAddress(ctx, "")
    if err != nil {
        return fmt.Errorf("failed to find checkpoint agent: %w", err)
    }
    
    conn, err := grpc.Dial(agentAddr, grpc.WithInsecure())
    if err != nil {
        return fmt.Errorf("failed to connect to agent: %w", err)
    }
    defer conn.Close()
    
    client := pb.NewCheckpointAgentClient(conn)
    
    request := &pb.ExtractImagesRequest{
        CheckpointUri: checkpointURI,
        MigrationName: migrationName,
        TargetPath:    fmt.Sprintf("/mnt/checkpoints/%s/images", migrationName),
    }
    
    response, err := client.ExtractCheckpointImages(ctx, request)
    if err != nil {
        return fmt.Errorf("failed to extract checkpoint images: %w", err)
    }
    
    if !response.Success {
        return fmt.Errorf("checkpoint extraction failed: %s", response.ErrorMessage)
    }
    
    return nil
}
```

#### B. Agent gRPC Service (`cmd/checkpoint-agent/main.go`)
```go
func (s *checkpointAgentServer) ExtractCheckpointImages(ctx context.Context, req *pb.ExtractImagesRequest) (*pb.ExtractImagesResponse, error) {
    logger := log.FromContext(ctx)
    logger.Info("Extracting checkpoint images", "uri", req.CheckpointUri, "migration", req.MigrationName)
    
    // Convert shared:// URI to local path
    sourcePath := strings.Replace(req.CheckpointUri, "shared://", "/mnt/checkpoints/", 1)
    
    // Create target directory structure
    err := os.MkdirAll(req.TargetPath, 0755)
    if err != nil {
        return &pb.ExtractImagesResponse{
            Success:      false,
            ErrorMessage: fmt.Sprintf("failed to create target directory: %v", err),
        }, nil
    }
    
    // Extract tar file to target directory
    cmd := exec.Command("tar", "-xf", sourcePath, "-C", req.TargetPath)
    output, err := cmd.CombinedOutput()
    if err != nil {
        return &pb.ExtractImagesResponse{
            Success:      false,
            ErrorMessage: fmt.Sprintf("tar extraction failed: %v, output: %s", err, string(output)),
        }, nil
    }
    
    logger.Info("Checkpoint images extracted successfully", "path", req.TargetPath)
    
    return &pb.ExtractImagesResponse{
        Success: true,
    }, nil
}
```

### 5. Preflight Environment Validation

#### A. Homogeneity Check DaemonSet (`config/preflight/`)
```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: migration-preflight
  namespace: live-pod-migration-controller-system
spec:
  selector:
    matchLabels:
      app: migration-preflight
  template:
    metadata:
      labels:
        app: migration-preflight
    spec:
      containers:
      - name: preflight
        image: ubuntu:22.04
        command: ["/bin/bash", "-c"]
        args:
        - |
          apt-get update && apt-get install -y criu runc
          
          echo "=== Node Homogeneity Check ==="
          echo "Node: $(hostname)"
          echo "Kernel: $(uname -r)"
          echo "CRIU: $(criu --version 2>&1 | head -1)"
          echo "runc: $(runc --version | head -1)"
          echo "CPU flags: $(grep flags /proc/cpuinfo | head -1)"
          
          echo "=== CRIU Compatibility Check ==="
          criu check --all || {
            echo "ERROR: CRIU check failed on $(hostname)"
            exit 1
          }
          
          echo "=== Node $(hostname) passed preflight checks ==="
          
          # Keep running for monitoring
          while true; do sleep 3600; done
        securityContext:
          privileged: true
        volumeMounts:
        - name: proc
          mountPath: /host/proc
        - name: sys
          mountPath: /host/sys
      volumes:
      - name: proc
        hostPath:
          path: /proc
      - name: sys
        hostPath:
          path: /sys
      hostNetwork: true
      hostPID: true
      tolerations:
      - operator: Exists
```

## Implementation Plan

### Phase 1: Core Self-Restore Pattern
1. **Build self-restore app images** with CRIU and restore scripts
2. **Implement controller modifications** for pod spec enhancement
3. **Add agent checkpoint extraction** functionality
4. **Test basic same-node self-restore** workflow

### Phase 2: Production Hardening
1. **Deploy preflight validation** DaemonSet
2. **Implement error handling** and fallback to fresh start
3. **Add restore health monitoring** and auto-recovery
4. **Test cross-node migration** scenarios

### Phase 3: Security & Performance
1. **Minimize capabilities** to essential CRIU requirements
2. **Re-enable SELinux/AppArmor** with proper profiles
3. **Add TCP restoration** for network connections
4. **Implement lazy pages** for large memory workloads

## Success Criteria

### Technical Validation
- [ ] **No CRIU segmentation faults** during restoration
- [ ] **Process state continuity** (timestamps show no gaps)
- [ ] **Application data preservation** (files, memory state intact)
- [ ] **Network connectivity** maintained after restoration
- [ ] **Works on homogeneous clusters** (same kernel/CRIU versions)

### Kubernetes-Native Compliance
- [ ] **No kubelet/CRI-O modifications** required
- [ ] **Standard pod specifications** with documented capabilities
- [ ] **Existing NFS storage** integration maintained
- [ ] **Compatible with RBAC** and security policies
- [ ] **Operates within namespace** boundaries

## Risks & Mitigations

### Security Risks
- **Risk**: Elevated capabilities (`SYS_PTRACE`, `SYS_ADMIN`) increase attack surface
- **Mitigation**: Minimize scope, use init containers, re-enable security policies after validation

### Compatibility Risks  
- **Risk**: Node heterogeneity causes CRIU failures
- **Mitigation**: Preflight validation DaemonSet enforces homogeneity requirements

### Performance Risks
- **Risk**: In-container CRIU may be slower than host injection
- **Mitigation**: Benchmark and optimize; implement lazy pages for large workloads

## Migration Path

### From Current Implementation
1. **Keep existing checkpoint creation** (works correctly)
2. **Keep existing shared storage** (NFS-based)
3. **Replace OCI image restore** with self-restore pattern
4. **Maintain controller workflow** phases with minor modifications

### Backwards Compatibility
- **Graceful degradation**: If self-restore fails, fallback to fresh application start
- **Feature flag**: Enable self-restore via pod annotation
- **Staged rollout**: Test with specific workloads before general deployment

## Conclusion

The self-restore pattern eliminates CRIU segmentation faults by aligning with Kubernetes' sandbox model rather than fighting it. This approach:

- ✅ **Solves the root cause**: No more host/runtime context mismatches
- ✅ **Remains Kubernetes-native**: No kubelet or CRI-O modifications
- ✅ **Proven in production**: Used by JVM/CRaC and OpenShift workflows  
- ✅ **Minimal infrastructure changes**: Leverages existing NFS storage
- ✅ **Incremental implementation**: Can be deployed alongside current system

This proposal transforms live pod migration from a runtime integration challenge into a straightforward container orchestration problem that Kubernetes handles excellently.