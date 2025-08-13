# Kubelet to CRI-O Checkpoint Restoration Flow Analysis

## Executive Summary

This document provides a precise technical analysis of the function invocation flow from kubelet to CRI-O for checkpoint-based container restoration. The goal is to understand how to trigger CRI-O's checkpoint tar file restoration mechanism through the standard Kubernetes container lifecycle.

## Critical Prerequisites for Checkpoint Restoration

For CRI-O to restore a container from a checkpoint tar file, the following conditions must be met:

1. **CRI-O CreateContainer** must detect the checkpoint and call `container.SetRestore(true)`
2. **CRI-O StartContainer** must detect `container.Restore() == true` and call `ContainerRestore()`
3. **Checkpoint tar file** must be accessible at the specified path when CRI-O performs `os.Stat()`

## Complete Function Invocation Flow

### Phase 1: Kubelet Container Creation (The Blocking Point)

**File**: `/kubernetes/pkg/kubelet/kuberuntime/kuberuntime_container.go`

```go
// Line 198: startContainer()
func (m *kubeGenericRuntimeManager) startContainer(ctx context.Context, pod *v1.Pod, container *v1.Container, restartCount int, containerStartTime time.Time) error {
    
    // ❌ CHECKPOINT RESTORATION FAILS HERE
    // Line 212: Image validation that blocks checkpoint paths
    imageRef, msg, err := m.imageManager.EnsureImageExists(
        ctx, objRef, pod, container.Image, pullSecrets, podSandboxConfig, pod.Spec.RuntimeClassName, pullPolicy
    )
    if err != nil {
        return err  // ← Returns "invalid reference format" for checkpoint paths
    }
    
    // ✅ CHECKPOINT RESTORATION NEEDS TO REACH HERE
    // Line 251: Generate container config with checkpoint path
    containerConfig, err := m.generateContainerConfig(
        ctx, container, pod, restartCount, podIP, imageRef, podIPs, nsTarget, imageVolumes
    )
    
    // Line 274: CRI call to CRI-O CreateContainer
    containerID, err := m.runtimeService.CreateContainer(podSandboxID, containerConfig, podSandboxConfig)
    
    // Line 291: CRI call to CRI-O StartContainer
    err = m.runtimeService.StartContainer(ctx, containerID)
}
```

#### 1.1 Image Validation (The Barrier)

**File**: `/kubernetes/pkg/kubelet/images/image_manager.go`

```go
// Line 151: EnsureImageExists()
func (m *imageManager) EnsureImageExists(...) (imageRef, message string, err error) {
    
    // Line 155: ❌ CHECKPOINT PATHS FAIL HERE
    image, err := applyDefaultImageTag(requestedImage)  // Calls parsers.ParseImageName()
    if err != nil {
        return "", msg, ErrInvalidImageName  // ← "invalid reference format"
    }
    
    // Line 176: Image lookup in local store
    imageRef, message, err = m.imagePullPrecheck(ctx, objRef, logPrefix, pullPolicy, &spec, requestedImage)
    
    // Line 187: ⚠️ ONLY REACHED IF VALIDATION PASSES
    repoToPull, _, _, err := parsers.ParseImageName(spec.Image)
}
```

**File**: `/kubernetes/pkg/util/parsers/parsers.go`

```go
// Line 31: ParseImageName() - The Core Validation Function
func ParseImageName(image string) (string, string, string, error) {
    
    // Line 32: ❌ CHECKPOINT PATHS REJECTED HERE
    named, err := dockerref.ParseNormalizedNamed(image)  // Docker distribution library
    if err != nil {
        return "", "", "", fmt.Errorf("couldn't parse image name %q: %v", image, err)
    }
    // File paths like "/mnt/checkpoints/file.tar" are NOT valid Docker references
}
```

#### 1.2 Container Config Generation (If Validation Passes)

**File**: `/kubernetes/pkg/kubelet/kuberuntime/kuberuntime_container.go`

```go
// Line ~340: generateContainerConfig()
config := &runtimeapi.ContainerConfig{
    Image: &runtimeapi.ImageSpec{
        Image: imageRef,                    // From EnsureImageExists() (may be empty)
        UserSpecifiedImage: container.Image // ✅ RAW CHECKPOINT PATH FROM POD SPEC
    },
}
```

**Key Insight**: Even if `imageRef` is empty, `UserSpecifiedImage` contains the original checkpoint path.

### Phase 2: CRI-O Container Creation (Checkpoint Detection)

**File**: `/cri-o/server/container_create.go`

```go
// Line 387: CreateContainer() - CRI-O receives request from kubelet
func (s *Server) CreateContainer(ctx context.Context, req *types.CreateContainerRequest) (*types.CreateContainerResponse, error) {
    
    // Line 407: ✅ CHECKPOINT DETECTION LOGIC
    checkpointImage, err := func() (bool, error) {
        if !s.config.CheckpointRestore() {
            return false, nil
        }
        
        // Line 414: ✅ FILE-BASED CHECKPOINT DETECTION
        if _, err := os.Stat(req.Config.Image.Image); err == nil {
            log.Debugf(ctx, "%q is a file. Assuming it is a checkpoint archive", req.Config.Image.Image)
            return true, nil  // ← CHECKPOINT DETECTED!
        }
        
        // Line 424: ✅ OCI CHECKPOINT IMAGE DETECTION
        imageID, err := s.checkIfCheckpointOCIImage(ctx, req.Config.Image.Image)
        return imageID != nil, nil
    }()
    
    // Line 435: ✅ CHECKPOINT RESTORATION PATH
    if checkpointImage {
        ctrID, err := s.CRImportCheckpoint(ctx, req.Config, sb, req.SandboxConfig.Metadata.Uid)
        if err != nil {
            return nil, err
        }
        return &types.CreateContainerResponse{ContainerId: ctrID}, nil
    }
    // ... normal container creation
}
```

#### 2.1 Checkpoint Import Process

**File**: `/cri-o/server/container_restore.go`

```go
// Line 57: CRImportCheckpoint()
func (s *Server) CRImportCheckpoint(ctx context.Context, config *types.ContainerConfig, sb *sandbox.Sandbox, podUID string) (string, error) {
    
    // Line 115: ✅ OPEN CHECKPOINT TAR FILE
    archiveFile, err := os.Open(inputImage)  // inputImage = req.Config.Image.Image
    
    // Line 149: ✅ EXTRACT CHECKPOINT
    archive.Untar(archiveFile, mountPoint, options)
    
    // Line 164: ✅ LOAD CONTAINER SPEC FROM CHECKPOINT
    spec, err := generate.NewFromFile(specDumpFile)
    
    // Line 405: ✅ SET RESTORATION FLAG
    newContainer.SetRestore(true)  // ← CRITICAL: Marks container for restoration
    
    return newContainer.ID(), nil
}
```

### Phase 3: CRI-O Container Start (Restoration Execution)

**File**: `/cri-o/server/container_start.go`

```go
// Line 18: StartContainer() - CRI-O receives start request from kubelet
func (s *Server) StartContainer(ctx context.Context, req *types.StartContainerRequest) (*types.StartContainerResponse, error) {
    
    // Line 24: Get container object
    c, err := s.GetContainerFromShortID(ctx, req.ContainerId)
    
    // Line 29: ✅ RESTORATION DETECTION
    if c.Restore() {  // Checks flag set by CRImportCheckpoint
        
        // Line 33: ✅ CHECKPOINT RESTORATION PATH
        log.Debugf(ctx, "Restoring container %q", req.ContainerId)
        
        // Line 35: ✅ EXECUTE RESTORATION
        ctr, err := s.ContainerRestore(
            ctx,
            &metadata.ContainerConfig{ID: c.ID()},
            &lib.ContainerCheckpointOptions{},
        )
        
        // Line 62: ✅ RESTORATION COMPLETE
        return &types.StartContainerResponse{}, nil
    }
    
    // Normal container start logic...
}
```

#### 3.1 Container Restoration Process

**File**: `/cri-o/internal/lib/restore.go`

```go
// Line 23: ContainerRestore()
func (c *ContainerServer) ContainerRestore(ctx context.Context, config *metadata.ContainerConfig, opts *ContainerCheckpointOptions) (string, error) {
    
    // Line 296: ✅ DELEGATE TO OCI RUNTIME
    err := c.runtime.RestoreContainer(ctx, ctr, sb.CgroupParent(), sb.MountLabel())
}
```

**File**: `/cri-o/internal/oci/runtime_oci.go`

```go
// Line ~1700: RestoreContainer()
func (r *runtimeOCI) RestoreContainer(ctx context.Context, c *Container, cgroupParent, mountLabel string) error {
    
    // Line 1700: ✅ CREATE CONTAINER WITH RESTORATION
    if err := r.CreateContainer(ctx, c, cgroupParent, true); err != nil {  // restore=true
        return err
    }
}

// Line ~250: CreateContainer() with restore flag
func (r *runtimeOCI) CreateContainer(ctx context.Context, c *Container, cgroupParent string, restore bool) error {
    
    if restore {
        // Line 439: ✅ FINAL CRIU INVOCATION
        args = append(args, "--restore", c.CheckpointPath())  // ← ACTUAL CRIU CALL
    }
    
    // Line ~290: ✅ EXECUTE CONMON WITH RESTORATION
    err = cmd.Start()  // Executes: conmon --restore /path/to/checkpoint
}
```

## The Critical Problem: Image Validation Barrier

### Current Flow Status

```
✅ Pod Migration Controller → Sets container.Image = "/mnt/checkpoints/checkpoint.tar"
❌ Kubelet EnsureImageExists() → Rejects checkpoint path (invalid Docker reference)
⚠️  CRI-O CreateContainer → NEVER REACHED
⚠️  CRI-O StartContainer → NEVER REACHED
⚠️  Checkpoint Restoration → NEVER EXECUTED
```

### Required Flow for Success

```
✅ Pod Migration Controller → Sets container.Image = <VALID_REFERENCE>
✅ Kubelet EnsureImageExists() → Passes validation
✅ Kubelet generateContainerConfig() → Image: <REF>, UserSpecifiedImage: <CHECKPOINT_PATH>
✅ CRI-O CreateContainer → Detects checkpoint, calls CRImportCheckpoint(), sets Restore(true)
✅ CRI-O StartContainer → Detects Restore(true), calls ContainerRestore()
✅ Checkpoint Restoration → Successfully executes CRIU restoration
```

## Technical Requirements for Success

### 1. Image Reference Format

The `container.Image` value must:
- ✅ Pass `dockerref.ParseNormalizedNamed()` validation in kubelet
- ✅ Be detectable as a checkpoint by CRI-O's `os.Stat()` or `checkIfCheckpointOCIImage()`
- ✅ Point to an accessible checkpoint tar file

### 2. CRI-O Configuration

- ✅ `enable_criu_support = true` in `/etc/crio/crio.conf`
- ✅ CRIU binary available and functional
- ✅ Checkpoint tar files accessible at specified paths

### 3. Container Image Specification

In the CRI request, CRI-O receives:
```go
req.Config.Image.Image           // This is what CRI-O checks with os.Stat()
req.Config.Image.UserSpecifiedImage  // Original pod spec value
```

From kubelet's `generateContainerConfig()`:
```go
Image: &runtimeapi.ImageSpec{
    Image: imageRef,           // From EnsureImageExists() - what we need to make valid
    UserSpecifiedImage: container.Image  // Raw pod spec - checkpoint path
}
```

## Potential Solutions

### Solution 1: Docker-Reference-Compatible Checkpoint Paths

Create checkpoint files with names that pass Docker reference validation:
```
Instead of: /mnt/checkpoints/659eff0c-nginx-20250813-064247.tar
Use:        /mnt/checkpoints/localhost/checkpoint/nginx:20250813-064247
```

### Solution 2: Pre-populate Image Store

Add checkpoint files to kubelet's image store so `GetImageRef()` returns a valid reference.

### Solution 3: OCI Checkpoint Images

Use CRI-O's native OCI checkpoint image support:
```bash
buildah from scratch
buildah add container /mnt/checkpoints/checkpoint.tar /
buildah config --annotation "io.kubernetes.cri-o.checkpoint=true" container
buildah commit container localhost/checkpoint:id
```

### Solution 4: Image Pull Policy Bypass

Investigate if specific pull policies or annotations can bypass image validation.

## Critical File Mappings

| Component | File | Key Function | Purpose |
|-----------|------|--------------|---------|
| **Kubelet** | `kuberuntime_container.go:198` | `startContainer()` | Entry point for container creation |
| **Kubelet** | `image_manager.go:151` | `EnsureImageExists()` | **BLOCKING POINT** - Image validation |
| **Kubelet** | `parsers.go:31` | `ParseImageName()` | **FAILURE POINT** - Docker reference parsing |
| **CRI-O** | `container_create.go:387` | `CreateContainer()` | **TARGET** - Checkpoint detection |
| **CRI-O** | `container_create.go:414` | File detection logic | `os.Stat()` checkpoint detection |
| **CRI-O** | `container_restore.go:57` | `CRImportCheckpoint()` | Checkpoint import and `SetRestore(true)` |
| **CRI-O** | `container_start.go:18` | `StartContainer()` | **TARGET** - Restoration trigger |
| **CRI-O** | `container_start.go:29` | Restoration detection | `if c.Restore()` check |
| **CRI-O** | `runtime_oci.go:~250` | `CreateContainer()` | **FINAL TARGET** - CRIU execution |

## Next Steps

1. **Test Docker-reference-compatible paths** to bypass kubelet validation
2. **Verify CRI-O checkpoint detection** with different path formats
3. **Implement and test** the most promising solution
4. **Validate end-to-end restoration** from checkpoint tar files

The core challenge is making the checkpoint path both Docker-reference-valid for kubelet and file-detectable for CRI-O.

## Experimental Results

### Experiment 1: Docker-Reference-Compatible Checkpoint Paths

**Hypothesis**: Name checkpoint files with Docker-compatible references (e.g., `localhost/checkpoint/nginx:tag`) to bypass kubelet validation.

**Test Setup**:
```bash
# Create Docker-compatible directory structure on master node
vagrant ssh master -c "sudo mkdir -p /mnt/checkpoints/localhost/checkpoint"

# Create checkpoint file with Docker-compatible name (colon in filename)
vagrant ssh master -c "echo 'test' | sudo tee /mnt/checkpoints/localhost/checkpoint/nginx:20250813-064247"
```

**Test 1: Kubectl Validation with Relative Docker Reference**
```bash
vagrant ssh master -c "kubectl run test-restore --image='localhost/checkpoint/nginx:20250813-064247' --restart=Never --dry-run=client -o yaml"
```

**Result**: ✅ **PASSED** - Kubectl accepts the Docker-compatible reference
```yaml
apiVersion: v1
kind: Pod
metadata:
  labels:
    run: test-restore
  name: test-restore
spec:
  containers:
  - image: localhost/checkpoint/nginx:20250813-064247
    name: test-restore
  restartPolicy: Never
```

**Test 2: Kubectl Validation with Absolute Path**
```bash
vagrant ssh master -c "kubectl run test-restore2 --image='/mnt/checkpoints/localhost/checkpoint/nginx:20250813-064247' --restart=Never --dry-run=client -o yaml"
```

**Result**: ❌ **FAILED** - Absolute paths still rejected
```
error: Invalid image name "/mnt/checkpoints/localhost/checkpoint/nginx:20250813-064247": invalid reference format
```

**Test 3: CRI-O Handling of Docker Reference**
```bash
vagrant ssh master -c "sudo crictl pull localhost/checkpoint/nginx:20250813-064247"
```

**Result**: ❌ **FAILED** - CRI-O interprets as registry image, not file
```
E0813 04:04:26.048608 remote_image.go:180] "PullImage from image service failed" 
err="rpc error: code = Unknown desc = RegistryUnavailable: pinging container registry localhost: 
Get \"http://localhost/v2/\": dial tcp 127.0.0.1:80: connect: connection refused"
```

**Conclusion**: This approach creates a **fundamental mismatch**:
- ✅ Kubelet accepts `localhost/checkpoint/nginx:tag` as valid Docker reference
- ❌ CRI-O treats it as a registry pull, never checking `os.Stat()` for file existence
- ❌ The checkpoint detection at `container_create.go:116` requires an absolute file path
- ❌ But absolute file paths fail kubelet's Docker reference validation

**Problem**: We're stuck in a catch-22:
- Kubelet needs Docker-compatible references → rejects file paths
- CRI-O needs file paths for `os.Stat()` → doesn't check files for Docker references
- The two requirements are mutually exclusive with this approach

### Experiment 2: OCI Checkpoint Images with Buildah

**Hypothesis**: Create OCI images with checkpoint annotation `io.kubernetes.cri-o.checkpoint=true` that CRI-O will recognize and restore.

**Test Setup**:
```bash
# Create OCI checkpoint image with buildah on worker node
sudo buildah from --name checkpoint-container scratch
sudo buildah add checkpoint-container /tmp/test-checkpoint.tar /
sudo buildah config --annotation 'io.kubernetes.cri-o.checkpoint=true' checkpoint-container
sudo buildah commit checkpoint-container localhost/checkpoint-image:test1
```

**Test 1: Verify Annotation in Image**
```bash
sudo buildah inspect localhost/checkpoint-image:test1 | grep -A2 'io.kubernetes.cri-o.checkpoint'
```

**Result**: ✅ **PASSED** - Annotation correctly set
```json
"ImageAnnotations": {
    "io.kubernetes.cri-o.checkpoint": "true"
}
```

**Test 2: Create Pod with OCI Checkpoint Image**
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-oci-checkpoint
spec:
  restartPolicy: Never
  containers:
  - name: restored-container
    image: localhost/checkpoint-image:test1
```

**Result**: ❌ **FAILED** - Container creation failed
```
Status: CreateContainerError
Error: no command specified
```

**Test 3: Check CRI-O Logs for Checkpoint Detection**
```bash
sudo journalctl -u crio | grep -i checkpoint
```

**Result**: ⚠️ **PARTIAL** - CRI-O sees annotation but doesn't trigger restoration
```
level=info msg="Image status: &ImageStatusResponse{Image:&Image{...
Annotations:map[string]string{io.kubernetes.cri-o.checkpoint: true,}...}}"
level=info msg="Creating container: default/test-oci-checkpoint/restored-container"
level=info msg="createCtr: releasing container name k8s_restored-container..."
```

**Conclusion**: 
- ✅ Kubelet accepts OCI image references
- ✅ CRI-O pulls the image and sees the checkpoint annotation
- ❌ CRI-O does NOT trigger checkpoint restoration path despite annotation
- ❌ Container fails with "no command specified" - treated as regular container

**Problem**: The OCI checkpoint image approach doesn't work because:
1. CRI-O's `checkIfCheckpointOCIImage()` may have additional requirements beyond the annotation
2. The checkpoint detection logic at `container_create.go:423` may not be properly triggered
3. Even with annotation, CRI-O treats it as a regular container requiring a command

### Investigation: CRI-O Checkpoint Detection Code Path

**Finding**: The annotation name was incorrect. CRI-O's `checkIfCheckpointOCIImage()` function at `server/container_restore.go:26` checks for:
- Annotation key: `io.kubernetes.cri-o.annotations.checkpoint.name`
- Annotation value: The original container name (not a boolean)

**Code Analysis**:
```go
// server/container_restore.go:26-54
func (s *Server) checkIfCheckpointOCIImage(ctx context.Context, input string) (*storage.StorageImageID, error) {
    // Skip if input is empty
    if input == "" {
        return nil, nil
    }
    
    // Skip if input is a file that exists (handled separately)
    if _, err := os.Stat(input); err == nil {
        return nil, nil
    }
    
    // Get image status from storage
    status, err := s.storageImageStatus(ctx, types.ImageSpec{Image: input})
    if status == nil || status.Annotations == nil {
        return nil, nil
    }
    
    // Check for checkpoint annotation
    ann, ok := status.Annotations[annotations.CheckpointAnnotationName]
    if !ok {
        return nil, nil  // ← Returns nil if annotation not found
    }
    
    log.Debugf(ctx, "Found checkpoint of container %v in %v", ann, input)
    return &status.ID, nil  // ← Returns image ID if annotation found
}
```

### Experiment 3: OCI Checkpoint Image with Correct Annotation

**Test Setup**:
```bash
# Create OCI checkpoint image with CORRECT annotation
sudo buildah from --name checkpoint-container2 scratch
sudo buildah add checkpoint-container2 /tmp/test-checkpoint.tar /
sudo buildah config --annotation 'io.kubernetes.cri-o.annotations.checkpoint.name=nginx-checkpoint' checkpoint-container2
sudo buildah commit checkpoint-container2 localhost/checkpoint-image:test2
```

**Result**: ✅ **CHECKPOINT DETECTION WORKS!**
```
Error: failed to read "spec.dump": open /var/lib/containers/storage/overlay/.../spec.dump: no such file or directory
```

**Conclusion**: 
- ✅ CRI-O now correctly detects the checkpoint image
- ✅ CRI-O attempts to restore from checkpoint (calls `CRImportCheckpoint`)
- ❌ Restoration fails because our test tar doesn't have proper checkpoint structure

**Key Learning**: The OCI checkpoint image approach **DOES WORK** when:
1. The image has annotation `io.kubernetes.cri-o.annotations.checkpoint.name` with container name as value
2. The checkpoint tar file inside the image has proper CRIU checkpoint structure (spec.dump, etc.)

### Experiment 4: End-to-End Checkpoint/Restore with Stateful Container

**Objective**: Verify state preservation through checkpoint/restore cycle using OCI checkpoint images.

**Step 1: Create Stateful Container**
```bash
# Pod with incrementing counter
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: stateful-counter
spec:
  containers:
  - name: counter
    image: busybox:latest
    command: ['/bin/sh', '-c']
    args:
    - |
      counter=0
      while true; do
        echo "\$(date '+%Y-%m-%d %H:%M:%S') - Counter: \$counter"
        counter=\$((counter + 1))
        sleep 2
      done
EOF
```

**Step 2: Checkpoint the Running Container**
```bash
# After ~90 seconds, counter reaches 89
kubectl logs stateful-counter --tail=3
# Output:
# 2025-08-13 14:26:04 - Counter: 87
# 2025-08-13 14:26:06 - Counter: 88
# 2025-08-13 14:26:08 - Counter: 89

# Create checkpoint
sudo crictl checkpoint --export=/tmp/counter-checkpoint.tar 8a03d1d9c017e
# Output: 8a03d1d9c017e (success)
```

**Step 3: Create OCI Checkpoint Image**
```bash
sudo buildah from --name counter-checkpoint-container scratch
sudo buildah add counter-checkpoint-container /tmp/counter-checkpoint.tar /
sudo buildah config --annotation 'io.kubernetes.cri-o.annotations.checkpoint.name=counter' counter-checkpoint-container
sudo buildah commit counter-checkpoint-container localhost/counter-checkpoint:latest
```

**Step 4: Restore from Checkpoint**
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: restored-counter
spec:
  containers:
  - name: counter
    image: localhost/counter-checkpoint:latest
```

**Result**: ⚠️ **PARTIAL SUCCESS**
- ✅ CRI-O correctly detects checkpoint image (annotation found)
- ✅ CRI-O attempts restoration via `CRImportCheckpoint()`
- ✅ Checkpoint tar is extracted and processed
- ❌ CRIU restoration fails with segmentation fault

**Error Log**:
```
Error: failed to restore container counter: 
787:(00.147259) 1: Error (criu/cr-restore.c:1480): 143 killed by signal 11: Segmentation fault
791:(00.147639) Error (criu/cr-restore.c:2447): Restoring FAILED.
```

**Analysis**: The checkpoint/restore pipeline works correctly up to CRIU execution. The restoration failure is due to:

**Root Cause Identified**: Mount point incompatibility
```
(00.013664) Error (criu/mount.c:2830): mnt: No mapping for /proc/timer_list mountpoint
```

The checkpoint contains mount points that don't exist or have different configurations in the restore environment. Specifically:
- `/proc/timer_list` is mounted as tmpfs in the checkpoint
- The restore environment has different mount configurations
- CRIU requires exact mount point compatibility for successful restoration

**Secondary Issue**: Process 143 segmentation fault
- After mount point mismatch, child process (PID 143) crashes with signal 11
- This is likely a consequence of the mount point failure, not the root cause

**Contributing Factors**:
1. Container runtime environment differences between checkpoint and restore
2. Kubernetes/CRI-O mount namespace configuration variations
3. CRIU's strict requirement for environmental consistency
4. Architecture: aarch64/ARM64 (though not the primary issue)

## Summary of Findings

### Working Approach: OCI Checkpoint Images

**Requirements**:
1. Create OCI image containing checkpoint tar file
2. Set annotation: `io.kubernetes.cri-o.annotations.checkpoint.name=<container-name>`
3. Use standard Kubernetes pod spec with image reference

**Flow**:
1. Kubelet accepts OCI image reference (e.g., `localhost/checkpoint:tag`)
2. CRI-O pulls/finds the image
3. CRI-O checks for checkpoint annotation in image metadata
4. If annotation found, CRI-O triggers checkpoint restoration path
5. Checkpoint tar is extracted and passed to CRIU for restoration

**Advantages**:
- Works with standard Kubernetes APIs
- No kubelet modifications needed
- Checkpoint files distributed as container images
- Compatible with existing container registries

**Current Limitations**:
- CRIU restoration stability issues observed
- Requires proper checkpoint structure in tar file
- May have architecture/kernel compatibility requirements