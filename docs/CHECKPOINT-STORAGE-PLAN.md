# Checkpoint Storage Plan: PVC-Based Shared Storage

## Overview

This document outlines the plan to implement shared storage for checkpoint files using Persistent Volume Claims (PVCs) to enable cross-node pod migration. The current implementation stores checkpoint files locally on each node, making them inaccessible to destination nodes during migration.

## Problem Statement

**Current State:**
- Checkpoint files are stored locally at `/var/lib/kubelet/checkpoints/` on each node
- Files are only accessible to the node that created them
- Cross-node migration requires transferring checkpoint data over the network
- No centralized storage for checkpoint artifacts

**Target State:**
- Checkpoint files stored in shared storage accessible from all nodes
- Immediate availability of checkpoint artifacts across the cluster
- Simplified migration logic with no explicit transfer step
- Centralized cleanup and garbage collection

## Design Requirements

### Feasibility & Prerequisites

1. **ReadWriteMany (RWX) Storage**: Requires a StorageClass that supports RWX access mode
2. **Cluster-wide Access**: All checkpoint agent pods must be able to mount the same volume
3. **Performance**: Storage backend must handle concurrent reads/writes from multiple nodes
4. **Consistency**: Atomic operations to prevent partial reads during checkpoint creation

### Storage Backend Options

#### Option 1: NFS-based Storage (Recommended for PoC)
- **Implementation**: Deploy `nfs-subdir-external-provisioner` 
- **Backing Store**: HostPath on dedicated VM or master node
- **Pros**: Simple setup, widely supported, good for development
- **Cons**: Single point of failure, limited performance

#### Option 2: Distributed Storage (Production)
- **Options**: Longhorn, Ceph, or cloud-native solutions (EFS, Azure Files)
- **Pros**: High availability, better performance, production-ready
- **Cons**: More complex setup, resource overhead

## Architecture Design

### High-Level Components

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   Node A        │    │   Node B        │    │   Node C        │
│                 │    │                 │    │                 │
│ ┌─────────────┐ │    │ ┌─────────────┐ │    │ ┌─────────────┐ │
│ │Agent Pod    │ │    │ │Agent Pod    │ │    │ │Agent Pod    │ │
│ │             │ │    │ │             │ │    │ │             │ │
│ │/mnt/chkpts──┼─┼────┼─┼─/mnt/chkpts─┼─┼────┼─┼─/mnt/chkpts │ │
│ └─────────────┘ │    │ └─────────────┘ │    │ └─────────────┘ │
└─────────────────┘    └─────────────────┘    └─────────────────┘
         │                       │                       │
         └───────────────────────┼───────────────────────┘
                                 │
                    ┌─────────────────────┐
                    │  Shared RWX PVC     │
                    │  checkpoint-repo    │
                    │                     │
                    │  /mnt/checkpoints/  │
                    │  ├── <podUID-1>/    │
                    │  │   └── <chkpt-1>/ │
                    │  │       ├── nginx/  │
                    │  │       └── app/    │
                    │  └── <podUID-2>/    │
                    └─────────────────────┘
```

### Directory Structure

```
/mnt/checkpoints/
├── <podUID>/
│   └── <checkpointID>/
│       ├── <containerName>/
│       │   ├── dump.tar.zst          # CRIU checkpoint data
│       │   └── manifest.json         # Metadata
│       └── .checkpoint-complete      # Completion marker
└── .gc/                              # Garbage collection metadata
```

### Path Conventions

- **Base Path**: `/mnt/checkpoints` (mounted PVC)
- **Pod Directory**: `<podUID>` (using UID to avoid name collisions)
- **Checkpoint ID**: `<timestamp>-<short-hash>` (for uniqueness)
- **Container Path**: `<checkpointID>/<containerName>/dump.tar.zst`
- **Artifact URI**: `shared://<podUID>/<checkpointID>/<containerName>/dump.tar.zst`

## Implementation Plan

### Phase 1: Storage Infrastructure

#### 1.1 Deploy NFS Provisioner
```yaml
# nfs-provisioner-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nfs-subdir-external-provisioner
  namespace: kube-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nfs-subdir-external-provisioner
  template:
    metadata:
      labels:
        app: nfs-subdir-external-provisioner
    spec:
      serviceAccountName: nfs-subdir-external-provisioner
      containers:
      - name: nfs-subdir-external-provisioner
        image: registry.k8s.io/sig-storage/nfs-subdir-external-provisioner:v4.0.2
        volumeMounts:
        - name: nfs-client-root
          mountPath: /persistentvolumes
        env:
        - name: PROVISIONER_NAME
          value: nfs-subdir-external-provisioner
        - name: NFS_SERVER
          value: 10.211.55.175  # Master node IP
        - name: NFS_PATH
          value: /var/nfs/checkpoint-storage
      volumes:
      - name: nfs-client-root
        nfs:
          server: 10.211.55.175
          path: /var/nfs/checkpoint-storage
```

#### 1.2 Create StorageClass
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nfs-rwx
provisioner: nfs-subdir-external-provisioner
parameters:
  archiveOnDelete: "false"
```

#### 1.3 Create Shared PVC
```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: checkpoint-repo
  namespace: live-pod-migration-controller-system
spec:
  accessModes: ["ReadWriteMany"]
  resources:
    requests:
      storage: 20Gi
  storageClassName: nfs-rwx
```

### Phase 2: Agent Modifications

#### 2.1 Update DaemonSet Configuration
```yaml
# In config/agent/daemonset.yaml
volumeMounts:
  - name: checkpoint-repo
    mountPath: /mnt/checkpoints
  # ... existing mounts
volumes:
  - name: checkpoint-repo
    persistentVolumeClaim:
      claimName: checkpoint-repo
  # ... existing volumes
```

#### 2.2 Modify Checkpoint Agent Logic

**Write Operation (Source Node):**
```go
func (s *CheckpointServer) writeCheckpointToSharedStorage(podUID, checkpointID, containerName string, checkpointData []byte) (string, error) {
    // Create directory structure
    checkpointDir := filepath.Join("/mnt/checkpoints", podUID, checkpointID, containerName)
    if err := os.MkdirAll(checkpointDir, 0755); err != nil {
        return "", err
    }
    
    // Atomic write: temp file then rename
    tempFile := filepath.Join(checkpointDir, "dump.tar.zst.tmp")
    finalFile := filepath.Join(checkpointDir, "dump.tar.zst")
    
    // Write compressed data
    if err := writeCompressedCheckpoint(tempFile, checkpointData); err != nil {
        return "", err
    }
    
    // Write metadata
    manifest := CheckpointManifest{
        PodUID: podUID,
        CheckpointID: checkpointID,
        ContainerName: containerName,
        CreationTime: time.Now(),
        KubeletAPI: "v1",
    }
    manifestPath := filepath.Join(checkpointDir, "manifest.json")
    if err := writeJSON(manifestPath, manifest); err != nil {
        return "", err
    }
    
    // Atomic rename
    if err := os.Rename(tempFile, finalFile); err != nil {
        return "", err
    }
    
    // Create completion marker
    completionMarker := filepath.Join(checkpointDir, "../.checkpoint-complete")
    if err := touchFile(completionMarker); err != nil {
        return "", err
    }
    
    // Return shared URI
    relativePath := filepath.Join(podUID, checkpointID, containerName, "dump.tar.zst")
    return fmt.Sprintf("shared://%s", relativePath), nil
}
```

**Read Operation (Destination Node):**
```go
func (s *CheckpointServer) readCheckpointFromSharedStorage(artifactURI string) ([]byte, error) {
    // Parse shared:// URI
    if !strings.HasPrefix(artifactURI, "shared://") {
        return nil, fmt.Errorf("unsupported URI scheme: %s", artifactURI)
    }
    
    relativePath := strings.TrimPrefix(artifactURI, "shared://")
    fullPath := filepath.Join("/mnt/checkpoints", relativePath)
    
    // Check completion marker
    checkpointDir := filepath.Dir(fullPath)
    parentDir := filepath.Dir(checkpointDir)
    completionMarker := filepath.Join(parentDir, ".checkpoint-complete")
    if _, err := os.Stat(completionMarker); err != nil {
        return nil, fmt.Errorf("checkpoint not complete: %v", err)
    }
    
    // Read checkpoint data
    return readCompressedCheckpoint(fullPath)
}
```

### Phase 3: Controller Updates

#### 3.1 Update ContainerCheckpointContent Spec
```go
type ContainerCheckpointContentSpec struct {
    // ... existing fields
    ArtifactURI string `json:"artifactURI"`
    // Storage metadata
    StorageType string `json:"storageType,omitempty"` // "shared", "local", "s3", etc.
    ChecksumSHA256 string `json:"checksumSHA256,omitempty"`
    CompressedSize int64 `json:"compressedSize,omitempty"`
}
```

#### 3.2 URI Resolution Logic
```go
func (r *ContainerCheckpointReconciler) resolveArtifactURI(uri string) (string, error) {
    switch {
    case strings.HasPrefix(uri, "shared://"):
        // Already in shared storage format
        return uri, nil
    case strings.HasPrefix(uri, "file://"):
        // Legacy local file, needs migration
        return r.migrateToSharedStorage(uri)
    default:
        return "", fmt.Errorf("unsupported URI scheme: %s", uri)
    }
}
```

### Phase 4: Garbage Collection

#### 4.1 Cleanup CronJob
```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: checkpoint-gc
  namespace: live-pod-migration-controller-system
spec:
  schedule: "0 2 * * *"  # Daily at 2 AM
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: gc
            image: localhost/checkpoint-gc:latest
            volumeMounts:
            - name: checkpoint-repo
              mountPath: /mnt/checkpoints
            env:
            - name: RETENTION_HOURS
              value: "24"
          volumes:
          - name: checkpoint-repo
            persistentVolumeClaim:
              claimName: checkpoint-repo
          restartPolicy: OnFailure
```

#### 4.2 GC Logic
```go
func cleanupOldCheckpoints(retentionHours int) error {
    cutoff := time.Now().Add(-time.Duration(retentionHours) * time.Hour)
    
    return filepath.Walk("/mnt/checkpoints", func(path string, info os.FileInfo, err error) error {
        if err != nil {
            return err
        }
        
        // Look for manifest.json files
        if filepath.Base(path) == "manifest.json" {
            manifest, err := readCheckpointManifest(path)
            if err != nil {
                log.Printf("Error reading manifest %s: %v", path, err)
                return nil
            }
            
            // Check if checkpoint is old enough
            if manifest.CreationTime.Before(cutoff) {
                // Verify no corresponding CRD exists
                if !checkpointCRDExists(manifest.PodUID, manifest.CheckpointID) {
                    checkpointDir := filepath.Dir(path)
                    log.Printf("Cleaning up old checkpoint: %s", checkpointDir)
                    return os.RemoveAll(checkpointDir)
                }
            }
        }
        
        return nil
    })
}
```

## Performance Considerations

### Compression Strategy
- **Format**: Use zstd compression for checkpoint data
- **Benefits**: Reduces storage size by 60-80%, faster network I/O
- **Tradeoff**: Slight CPU overhead during checkpoint/restore

### Concurrency Controls
- **Write Lock**: Use `.inprogress` files during checkpoint creation
- **Atomic Operations**: Always use temp file + rename pattern
- **Read Verification**: Check completion markers before reading

### Storage Optimization
- **NFS Tuning**: Increase rsize/wsize to 1MB for better throughput
- **Directory Structure**: Minimize depth to reduce metadata overhead
- **Batch Operations**: Group related files in same directory

## Security Considerations

### Access Controls
- **PVC Access**: Mount read-write only in checkpoint agent pods
- **Directory Permissions**: Use 0755 for pod directories, 0644 for files
- **Process Isolation**: Run agents with dedicated service account

### Data Isolation
- **Pod Separation**: Use podUID for directory isolation
- **Namespace Boundaries**: Consider namespace prefixes for multi-tenancy
- **Encryption**: Plan for at-rest encryption for sensitive workloads

## Failure Modes & Mitigations

### Storage Outages
- **Symptom**: All migrations fail
- **Mitigation**: Document RTO, consider storage HA
- **Fallback**: Graceful degradation to local storage mode

### Partial Writes
- **Prevention**: Atomic rename pattern
- **Detection**: Completion marker files
- **Recovery**: Automatic retry with cleanup

### Storage Exhaustion
- **Prevention**: Aggressive garbage collection
- **Monitoring**: Add storage usage metrics
- **Alerting**: Warn at 80% utilization

## Migration Path

### Phase 1: Infrastructure (Week 1)
1. Deploy NFS provisioner on master node
2. Create StorageClass and PVC
3. Update agent DaemonSet configuration
4. Test basic read/write operations

### Phase 2: Agent Logic (Week 2)
1. Implement shared storage write path
2. Add compression support
3. Update checkpoint creation logic
4. Test cross-node accessibility

### Phase 3: Controller Integration (Week 3)
1. Update CRD specifications
2. Modify controller URI handling
3. Implement artifact resolution
4. End-to-end testing

### Phase 4: Production Readiness (Week 4)
1. Add garbage collection
2. Implement monitoring
3. Performance tuning
4. Documentation updates

## Testing Strategy

### Unit Tests
- Checkpoint read/write operations
- Compression/decompression logic
- URI parsing and resolution
- Error handling scenarios

### Integration Tests
- Cross-node checkpoint access
- Concurrent read/write operations
- Garbage collection effectiveness
- Storage failure scenarios

### Performance Tests
- Checkpoint creation latency
- Storage throughput limits
- Concurrent migration scenarios
- Large checkpoint handling

## Monitoring & Observability

### Metrics
- Checkpoint creation duration
- Storage utilization percentage
- Failed checkpoint operations
- Garbage collection effectiveness

### Logs
- Checkpoint lifecycle events
- Storage access errors
- Performance warnings
- Cleanup operations

### Alerts
- Storage near capacity
- Checkpoint creation failures
- Unusually long operations
- Storage backend unavailable

## Future Enhancements

### Short Term
- **Incremental Checkpoints**: Delta-based storage for large containers
- **Deduplication**: Content-addressed storage for common layers
- **Compression Tuning**: Dynamic compression based on content type

### Long Term
- **Object Storage**: S3-compatible backend for cloud portability
- **Encryption**: Per-checkpoint encryption keys
- **Multi-Cluster**: Cross-cluster checkpoint federation
- **Streaming**: Reduce memory overhead for large checkpoints

## References

- [Kubernetes Storage Classes](https://kubernetes.io/docs/concepts/storage/storage-classes/)
- [NFS Subdir External Provisioner](https://github.com/kubernetes-sigs/nfs-subdir-external-provisioner)
- [CRIU Documentation](https://criu.org/Main_Page)
- [Kubelet Checkpoint API](https://kubernetes.io/docs/reference/node/kubelet-checkpoint-api/)