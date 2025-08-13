# Live Pod Migration - Restore Implementation Plan (Option 2: Kubernetes-Native)

## Overview

This document outlines the Kubernetes-native approach for checkpoint restoration. Instead of bypassing kubelet, we use the standard Kubernetes API Server workflow while leveraging CRI-O's built-in checkpoint restoration capabilities.

## Architecture: Kubernetes API Server Approach

### Complete Workflow

```
PodMigration Controller → Kubernetes API Server → kubelet → CRI-O → Checkpoint Restoration
```

**Key Insight**: CRI-O can restore from checkpoint images when they're specified in pod manifests submitted through the normal Kubernetes workflow.

## Concrete Code Path Evidence

### 1. Normal Kubernetes Pod Creation Flow

**Standard Flow**:
1. `kubectl apply` → API Server
2. API Server stores pod spec in etcd  
3. kubelet watches API Server via `/api/v1/pods`
4. kubelet calls `HandlePodAdditions()` for new pods
5. kubelet → CRI-O via gRPC (`RunPodSandbox`, `CreateContainer`, `StartContainer`)

**Our Checkpoint Flow**:
1. PodMigration Controller → API Server with checkpoint image pod spec
2. API Server stores pod spec in etcd
3. kubelet receives pod with checkpoint image
4. kubelet → CRI-O `CreateContainer` with checkpoint image path
5. CRI-O detects checkpoint image → triggers restoration

### 2. Kubelet Pod Processing

**File**: `kubernetes/pkg/kubelet/kubelet.go`

**HandlePodAdditions** (called when kubelet receives new pods):
```go
func (kl *Kubelet) HandlePodAdditions(pods []*v1.Pod) {
    for _, pod := range pods {
        // Process each new pod
        kl.podManager.AddPod(pod)
        kl.dispatchWork(pod, kubetypes.SyncPodCreate, mirrorPod, start)
    }
}
```

**SyncPod Implementation** (creates containers):
```go
func (kl *Kubelet) syncPod(ctx context.Context, updateType kubetypes.SyncPodType, pod, mirrorPod *v1.Pod, podStatus *kubecontainer.PodStatus) error {
    // ... validation and setup ...
    
    // Start containers in pod
    if err := kl.containerRuntime.SyncPod(pod, podStatus, pullSecrets, kl.backOff); err != nil {
        return err
    }
}
```

### 3. Container Runtime Integration

**File**: `kubernetes/pkg/kubelet/kuberuntime/kuberuntime_manager.go`

**SyncPod calls startContainer**:
```go
func (m *kubeGenericRuntimeManager) startContainer(podSandboxID string, podSandboxConfig *runtimeapi.PodSandboxConfig, spec *startSpec) error {
    container := spec.container
    
    // Pull image (this is where checkpoint image gets processed)
    imageRef, err := m.imagePuller.EnsureImageExists(spec.container.Image, pullSecrets, podSandboxConfig)
    
    // Create container config
    containerConfig, err := m.generateContainerConfig(container, pod, restartCount, podIP, imageRef, podIPs, target.ContainerID)
    
    // Create container via CRI
    containerID, err := m.runtimeService.CreateContainer(podSandboxID, containerConfig, podSandboxConfig)
    
    // Start container
    err = m.runtimeService.StartContainer(containerID)
}
```

### 4. The Key: Image Processing for Checkpoints

**Challenge**: Normal image pulling expects registry images, but we need local checkpoint files.

**Solution**: Use OCI-compliant checkpoint images that reference local files.

## Implementation Strategy

### Method 1: OCI Checkpoint Images (Recommended)

**Convert checkpoint tar files to OCI image format that CRI-O recognizes:**

```yaml
# Pod spec with OCI checkpoint image
apiVersion: v1
kind: Pod
metadata:
  name: restored-pod
spec:
  containers:
  - name: nginx
    image: "checkpoint-registry.local/checkpoints:stateful-pod-nginx-20250719-101556"
    # CRI-O will detect this as checkpoint image and restore
```

### Method 2: Local File Path with Image Pull Policy

**Use local file paths with specific image pull policy:**

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: restored-pod
spec:
  containers:
  - name: nginx
    image: "/mnt/checkpoints/3476063c-4cce-43fc-b03f-6276aa26e6f1-nginx-20250719-101556.tar"
    imagePullPolicy: Never  # Skip image pulling
```

## Implementation Details

### 1. PodMigration Controller Restore Implementation

**Add restore phase that creates pods via Kubernetes API:**

```go
func (r *PodMigrationReconciler) handleRestorePhase(ctx context.Context, migration *lpmv1.PodMigration) (ctrl.Result, error) {
    // 1. Get original pod and checkpoint content
    originalPod, err := r.getOriginalPod(ctx, migration)
    if err != nil {
        return ctrl.Result{}, err
    }
    
    checkpointContent, err := r.getCheckpointContent(ctx, migration)
    if err != nil {
        return ctrl.Result{}, err
    }
    
    // 2. Create restored pod manifest with checkpoint images
    restoredPod := r.createRestoredPodManifest(originalPod, migration, checkpointContent)
    
    // 3. Submit pod to Kubernetes API Server (not directly to kubelet)
    err = r.Create(ctx, restoredPod)
    if err != nil {
        return ctrl.Result{}, fmt.Errorf("failed to create restored pod: %v", err)
    }
    
    // 4. Monitor pod creation and readiness
    return ctrl.Result{RequeueAfter: 5 * time.Second}, r.updatePhase(ctx, migration, 
        lpmv1.PodMigrationPhaseRestoreInProgress, "pod submitted for restoration")
}

func (r *PodMigrationReconciler) handleRestoreInProgress(ctx context.Context, migration *lpmv1.PodMigration) (ctrl.Result, error) {
    restoredPodName := fmt.Sprintf("%s-restored", migration.Spec.PodName)
    
    var restoredPod corev1.Pod
    err := r.Get(ctx, client.ObjectKey{
        Name:      restoredPodName,
        Namespace: migration.Namespace,
    }, &restoredPod)
    
    if err != nil {
        return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
    }
    
    // Check if pod is running (restoration successful)
    if restoredPod.Status.Phase == corev1.PodRunning {
        return ctrl.Result{}, r.updatePhase(ctx, migration, lpmv1.PodMigrationPhaseSucceeded, 
            "pod successfully restored and running")
    }
    
    // Check for failures
    if restoredPod.Status.Phase == corev1.PodFailed {
        return ctrl.Result{}, r.updatePhase(ctx, migration, lpmv1.PodMigrationPhaseFailed, 
            "pod restoration failed")
    }
    
    return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}
```

### 2. Restored Pod Manifest Generation

**Create pod manifests with checkpoint images:**

```go
func (r *PodMigrationReconciler) createRestoredPodManifest(
    originalPod *corev1.Pod, 
    migration *lpmv1.PodMigration, 
    checkpointContent *lpmv1.PodCheckpointContent,
) *corev1.Pod {
    restoredPod := &corev1.Pod{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("%s-restored", originalPod.Name),
            Namespace: originalPod.Namespace,
            Labels:    originalPod.Labels,
            Annotations: map[string]string{
                "migration.source-pod":       originalPod.Name,
                "migration.target-node":      migration.Spec.TargetNode,
                "migration.checkpoint-source": checkpointContent.Name,
                "migration.timestamp":        time.Now().Format(time.RFC3339),
            },
        },
        Spec: corev1.PodSpec{
            NodeName:           migration.Spec.TargetNode,  // Force scheduling to target node
            RestartPolicy:      corev1.RestartPolicyNever, // Don't restart restored containers
            ServiceAccountName: originalPod.Spec.ServiceAccountName,
            SecurityContext:    originalPod.Spec.SecurityContext,
            Volumes:           originalPod.Spec.Volumes,    // Preserve volumes
            Containers:        make([]corev1.Container, len(originalPod.Spec.Containers)),
        },
    }
    
    // Replace container images with checkpoint images/paths
    for i, container := range originalPod.Spec.Containers {
        restoredContainer := container.DeepCopy()
        
        // Get checkpoint URI for this container
        checkpointURI := r.findCheckpointForContainer(checkpointContent, container.Name)
        
        // Convert to checkpoint image reference
        checkpointImage := r.convertToCheckpointImage(checkpointURI)
        restoredContainer.Image = checkpointImage
        restoredContainer.ImagePullPolicy = corev1.PullNever  // Don't pull checkpoint images
        
        restoredPod.Spec.Containers[i] = *restoredContainer
    }
    
    return restoredPod
}
```

### 3. Checkpoint Image Conversion

**Convert shared storage URIs to checkpoint image references:**

```go
func (r *PodMigrationReconciler) convertToCheckpointImage(checkpointURI string) string {
    // Option 1: Local file path
    if strings.HasPrefix(checkpointURI, "shared://") {
        filename := strings.TrimPrefix(checkpointURI, "shared://")
        return filepath.Join("/mnt/checkpoints", filename)
    }
    
    // Option 2: OCI checkpoint image (if using checkpoint registry)
    // return fmt.Sprintf("checkpoint-registry.local/checkpoints:%s", 
    //     strings.TrimSuffix(filename, ".tar"))
    
    return checkpointURI
}

func (r *PodMigrationReconciler) findCheckpointForContainer(
    checkpointContent *lpmv1.PodCheckpointContent, 
    containerName string,
) string {
    for _, containerContent := range checkpointContent.Spec.ContainerContents {
        // Get ContainerCheckpointContent to find the artifact URI
        var content lpmv1.ContainerCheckpointContent
        err := r.Get(context.TODO(), client.ObjectKey{
            Name:      containerContent.Name,
            Namespace: checkpointContent.Namespace,
        }, &content)
        if err != nil {
            continue
        }
        
        // Check if this is the right container by examining the name pattern
        if strings.Contains(content.Name, containerName) {
            return content.Spec.ArtifactURI
        }
    }
    return ""
}
```

### 4. Enhanced PodMigration Controller Phases

**Add new phases for Kubernetes-native restoration:**

```go
// Add to PodMigration types
const (
    PodMigrationPhaseRestoreSubmitted  PodMigrationPhase = "RestoreSubmitted"  // Pod submitted to API server
    PodMigrationPhaseRestoreInProgress PodMigrationPhase = "RestoreInProgress" // Pod being restored by kubelet
    PodMigrationPhaseRestoreCompleted  PodMigrationPhase = "RestoreCompleted"  // Pod running successfully
)

// Update reconcile logic
func (r *PodMigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // ... existing phases ...
    
    case lpmv1.PodMigrationPhaseCheckpointCompleted:
        return r.handleRestorePhase(ctx, &podMigration)
    case lpmv1.PodMigrationPhaseRestoreSubmitted:
        return r.handleRestoreInProgress(ctx, &podMigration)
    case lpmv1.PodMigrationPhaseRestoreInProgress:
        return r.handleRestoreInProgress(ctx, &podMigration)
    case lpmv1.PodMigrationPhaseRestoreCompleted:
        return r.handleMigrationSuccess(ctx, &podMigration)
}
```

## Advantages of Option 2

### 1. **Kubernetes-Native Integration**
- ✅ Pods appear in `kubectl get pods`
- ✅ Full integration with Kubernetes RBAC, admission controllers, etc.
- ✅ Works with services, ingress, monitoring, etc.
- ✅ Standard pod lifecycle management

### 2. **Leverage Existing Infrastructure**
- ✅ Uses standard API Server → kubelet → CRI-O flow
- ✅ No need to bypass or modify kubelet
- ✅ Benefits from kubelet's error handling and retry logic
- ✅ Integrates with existing logging and monitoring

### 3. **Clean Architecture**
- ✅ Controller only interacts with Kubernetes API
- ✅ No direct CRI calls or socket connections
- ✅ Standard Kubernetes patterns and practices
- ✅ Easy to test and debug

## Implementation Steps

### Step 1: Verify CRI-O Checkpoint Image Support

```bash
# Test if CRI-O can handle checkpoint images
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-checkpoint-restore
spec:
  nodeName: k8s-worker
  restartPolicy: Never
  containers:
  - name: nginx
    image: "/mnt/checkpoints/3476063c-4cce-43fc-b03f-6276aa26e6f1-nginx-20250719-101556.tar"
    imagePullPolicy: Never
EOF

# Monitor pod creation
kubectl get pod test-checkpoint-restore -w
kubectl describe pod test-checkpoint-restore
```

### Step 2: Implement Controller Restore Logic

1. **Add restore phases** to PodMigration CRD
2. **Implement handleRestorePhase** that creates pod manifests
3. **Add checkpoint image conversion** logic
4. **Test with existing checkpoint files**

### Step 3: Test End-to-End Migration

```bash
# 1. Create and checkpoint a pod
kubectl apply -f config/samples/test-pod.yaml

kubectl apply -f - <<EOF
apiVersion: lpm.my.domain/v1
kind: PodCheckpoint
metadata:
  name: test-checkpoint
spec:
  podName: test-pod
EOF

# 2. Wait for checkpoint completion
kubectl wait --for=condition=ready podcheckpoint/test-checkpoint --timeout=60s

# 3. Trigger migration
kubectl apply -f - <<EOF
apiVersion: lpm.my.domain/v1
kind: PodMigration
metadata:
  name: test-migration
spec:
  podName: test-pod
  targetNode: k8s-worker
EOF

# 4. Monitor migration progress
kubectl get podmigration test-migration -w
kubectl get pods -w
```

### Step 4: Handle Edge Cases

1. **Image pull failures**: Ensure `imagePullPolicy: Never` works correctly
2. **Node scheduling**: Verify `nodeName` forces correct scheduling
3. **Volume mounting**: Ensure shared storage access on target node
4. **Cleanup**: Delete original pod after successful restoration

## Alternative: OCI Checkpoint Images

If local file paths don't work reliably, implement OCI checkpoint image conversion:

### Checkpoint to OCI Image Converter

```go
func (r *PodMigrationReconciler) convertCheckpointToOCIImage(checkpointPath string) (string, error) {
    // Create OCI image from checkpoint tar file
    imageName := fmt.Sprintf("checkpoint-registry.local/checkpoints:%s", 
        strings.TrimSuffix(filepath.Base(checkpointPath), ".tar"))
    
    // Use buildah or similar to create OCI image
    cmd := exec.Command("buildah", "from", "scratch")
    cmd.Run()
    
    // Add checkpoint file as layer
    cmd = exec.Command("buildah", "add", "working-container", checkpointPath, "/checkpoint.tar")
    cmd.Run()
    
    // Commit as checkpoint image
    cmd = exec.Command("buildah", "commit", "working-container", imageName)
    cmd.Run()
    
    return imageName, nil
}
```

This approach provides the cleanest integration with Kubernetes while leveraging CRI-O's checkpoint restoration capabilities through the standard pod creation workflow.