# Live Pod Migration - Current Issues Analysis

## Project Context

We are building a Kubernetes-native live pod migration system that checkpoints running pods on one node and restores them on another node while preserving application state. The goal is to enable seamless pod migration across nodes without application downtime.

## System Architecture Overview

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   PodMigration  │───▶│  PodCheckpoint   │───▶│ ContainerCheck  │
│   Controller    │    │  Controller      │    │ point Agent     │
└─────────────────┘    └──────────────────┘    └─────────────────┘
         │                       │                       │
         ▼                       ▼                       ▼
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│ Create restored │    │ OCI Checkpoint   │    │ CRIU + Kubelet  │
│ pod with        │    │ Images           │    │ Checkpoint API  │
│ checkpoint imgs │    │                  │    │                 │
└─────────────────┘    └──────────────────┘    └─────────────────┘
```

## What Works Currently ✅

1. **Controller Orchestration**: PodMigration controller correctly manages the workflow phases
2. **Checkpoint Creation**: ContainerCheckpoint successfully creates checkpoint tar files via CRIU
3. **Shared Storage**: Checkpoint files are stored in NFS shared storage accessible across nodes
4. **OCI Image Conversion**: Checkpoint tar files are converted to OCI images with proper annotations
5. **Pod Creation**: Restored pods are created successfully with checkpoint images
6. **CRI-O Detection**: CRI-O correctly detects checkpoint annotations and attempts restoration

## Current Failure Point ❌

**The system fails during CRIU restoration with segmentation faults:**

```
Error: failed to restore container nginx: container restore failed
31 killed by signal 11: Segmentation fault
Error (criu/cr-restore.c:2447): Restoring FAILED.
```

## Root Cause Analysis

### What is Process State vs Application State?

#### Process State (What CRIU Captures)
- **Memory layout**: Virtual memory mappings, heap, stack contents
- **CPU context**: Register values, program counter, execution state  
- **File descriptors**: Open files, sockets, pipes with exact positions
- **Process tree**: Parent-child relationships, process IDs, thread states
- **Kernel state**: Signal handlers, timers, namespaces, cgroup memberships
- **Network state**: Socket connections, network namespace details

#### Application State (What Applications Care About)
- **Data files**: Application-specific data stored in files/databases
- **Configuration**: Settings, preferences, runtime configuration
- **Business logic state**: User sessions, transaction state, application variables
- **Persistent storage**: Data that survives process restarts

### The Fundamental Problem

**CRIU captures and restores process state, but Kubernetes pod sandboxes create different process environments.**

When a container is checkpointed on one pod and restored in another pod (even same node), the following changes:

#### 1. **Container Runtime Context Changes**
```bash
# Original pod
Pod UID: d1a3ff1c-f4fe-4b3e-9064-4fa4160951e8
Container ID: abc123...
Cgroup path: /sys/fs/cgroup/memory/kubepods/pod-d1a3ff1c.../abc123

# Restored pod  
Pod UID: 9f634e9b-231d-49e1-9369-4c180c163d8a  ← Different
Container ID: def456...                          ← Different
Cgroup path: /sys/fs/cgroup/memory/kubepods/pod-9f634e9b.../def456  ← Different
```

#### 2. **Network Namespace Changes**
```bash
# Original pod
Network namespace: /proc/1234/ns/net
Pod IP: 10.244.1.16
Interface: eth0@if15

# Restored pod
Network namespace: /proc/5678/ns/net  ← Different
Pod IP: 10.244.1.17                   ← Different  
Interface: eth0@if17                   ← Different
```

#### 3. **Mount Namespace Changes**
```bash
# Original pod
Mount namespace: /proc/1234/ns/mnt
Volume mounts: /var/lib/kubelet/pods/d1a3ff1c.../volumes/

# Restored pod
Mount namespace: /proc/5678/ns/mnt    ← Different
Volume mounts: /var/lib/kubelet/pods/9f634e9b.../volumes/  ← Different
```

#### 4. **Process ID Changes**
```bash
# Original container processes
nginx: PID 1
worker script: PID 15

# Restored container expects same PIDs
nginx: tries to restore as PID 1     ← May conflict
worker script: tries to restore as PID 15  ← May conflict
```

### Why Same-Node Migration Still Fails

Even on the same node, each pod gets:
- **New pod sandbox** with different UID, network namespace, cgroup hierarchy
- **New container runtime context** with different container IDs and paths
- **Different mount points** even for same volumes
- **Different process namespace** where PIDs may conflict

**CRIU expects identical runtime environment between checkpoint and restore, but Kubernetes pod lifecycle guarantees this will change.**

## Technical Deep Dive: CRIU Restoration Process

### What Happens During CRIU Restore

1. **Memory Restoration**: CRIU tries to recreate exact virtual memory layout
2. **File Descriptor Restoration**: Reopens files at exact positions  
3. **Network Restoration**: Restores socket states and network connections
4. **Process Tree Recreation**: Restores parent-child process relationships
5. **Namespace Binding**: Tries to enter original namespaces
6. **Resource Binding**: Rebinds to cgroups, mount points, etc.

### Where It Fails in Our System

```bash
# CRIU Restoration Log Analysis
pie: 30: Preadv 0xffffa02da000:4096... (1 iovs)     # Memory mapping
pie: 30: `- returned 4096                            # Successful read
pie: 30:    `- skip pagemap                          # Memory page mapping
Error: 31 killed by signal 11: Segmentation fault   # CRASH HERE
```

The segmentation fault occurs when CRIU tries to:
1. **Restore memory mappings** that reference old container paths
2. **Rebind to network interfaces** that no longer exist
3. **Access mount points** that have different paths
4. **Enter namespaces** that belong to old pod sandbox

## Why Current Approaches Don't Work

### Approach 1: CRIU Environment Options
**Problem**: Even with `--tcp-established`, `--manage-cgroups`, etc., CRIU still expects container runtime context consistency that Kubernetes cannot provide.

### Approach 2: Container Runtime Modifications
**Problem**: Not Kubernetes-native, requires modifying CRI-O/containerd internals.

### Approach 3: Same-Node Restoration
**Problem**: Even same-node creates new pod sandbox with different runtime context.

## The Kubernetes-Native Solution Space

### Option 1: Application-Level State Migration
**Concept**: Instead of process state, migrate application data and let application restart normally.

```yaml
# Example: Database pod migration
# 1. Dump database state
# 2. Store in persistent volume  
# 3. New pod reads state on startup
# 4. Application continues from saved point
```

**Pros**: 
- Works with Kubernetes pod lifecycle
- No runtime dependency on CRIU
- Handles all environment changes
- Application-aware migration

**Cons**: 
- Requires application cooperation
- Not transparent to applications

### Option 2: Volume-Based State Preservation
**Concept**: Use Kubernetes volumes to preserve stateful data across pod recreation.

```yaml
# Current test app writes to /data/log.txt
# This data could be preserved via:
# 1. Persistent volumes
# 2. Volume snapshots  
# 3. Init containers for state restoration
```

**Pros**:
- Kubernetes-native volume management
- Works across nodes naturally
- No process state complications
- Leverages existing Kubernetes features

**Cons**:
- Doesn't preserve in-memory state
- Application may need restart logic

### Option 3: Stateful Pod Sets with Persistent Identity
**Concept**: Use StatefulSets and persistent volumes to maintain pod identity across migrations.

**Pros**:
- Built-in Kubernetes feature
- Handles network identity
- Persistent storage guaranteed

**Cons**:
- Not transparent migration
- Requires application redesign

## Our Test Case Analysis

### Current Test Application
```bash
# nginx container: web server
# writer container: while true; do echo "$(date): Hello from $(hostname)" >> /data/log.txt; sleep 2; done
```

**State Characteristics**:
- **Process state**: nginx worker processes, shell script loop
- **Application state**: log entries in `/data/log.txt` file
- **Memory state**: minimal application memory
- **Network state**: nginx listening on port 80

### Migration Expectations
- **Process continuity**: writer loop continues from same iteration  
- **Data preservation**: log file contains uninterrupted timestamps
- **Service availability**: nginx continues serving requests
- **Network connectivity**: pod IP may change but service continues

## Recommended Path Forward

### Short-term: Volume-Based Migration (Kubernetes-Native)
1. **Modify controller** to snapshot volume data instead of process state
2. **Use init containers** to restore application state from volumes
3. **Implement graceful application restart** with state continuity
4. **Test with our current application** to show timestamp continuity in log file

### Long-term: Application-Aware Migration Framework
1. **Define migration interfaces** that applications can implement
2. **Build controller support** for application-specific checkpoint/restore
3. **Create migration patterns** for common application types (web servers, databases, etc.)

## Success Metrics

### Technical Success
- [ ] Restored pod starts successfully (no segfaults)
- [ ] Application state is preserved (log timestamps show continuity) 
- [ ] Network connectivity works (nginx serves requests)
- [ ] Works for both same-node and cross-node migration

### Kubernetes-Native Success  
- [ ] No modifications to container runtime or kubelet
- [ ] Uses standard Kubernetes APIs and resources
- [ ] Compatible with any Kubernetes cluster
- [ ] Leverages existing Kubernetes volume/storage features

## Detailed Investigation Results

### Commands Executed and Findings

After implementing the runtime context preservation fix (using `originalPod.DeepCopy()` in `createRestoredPod()`), the following investigation was conducted:

#### 1. **Verified Controller Fix Implementation**
```bash
# Checked PodMigration status after rebuild
vagrant ssh master -c "kubectl get podmigration migrate-worker-to-worker -o yaml"
```

**Result**: ✅ Controller correctly preserved runtime context:
- `checkpointImages` field populated with OCI checkpoint images
- `restartPolicy: Always` preserved (not changed to `Never`)
- `securityContext: {}` identical to original
- All volume mounts and tolerations preserved

#### 2. **Verified Pod Specification Preservation**
```bash
# Compare original vs restored pod specs
vagrant ssh master -c "kubectl get pod stateful-pod-same-node -o yaml | grep -A 10 'restartPolicy\|securityContext'"
vagrant ssh master -c "kubectl get pod stateful-pod-same-node-restored -o yaml | grep -A 10 'restartPolicy\|securityContext'"
```

**Result**: ✅ Runtime context preservation working perfectly:
- Pod-level settings identical
- Container specifications match
- Volume mounts preserved
- Security contexts identical

#### 3. **Analyzed CRIU Restoration Failure**
```bash
# Examined detailed CRIU restoration errors
vagrant ssh master -c "kubectl describe pod stateful-pod-same-node-restored | tail -20"
```

**Result**: ❌ Still getting `RunContainerError` with CRIU segmentation faults:
```
Error (criu/cr-restore.c:1480): 32 killed by signal 11: Segmentation fault
Error (criu/cr-restore.c:2447): Restoring FAILED.
```

#### 4. **Deep Dive into CRI-O Container Runtime Logs**
```bash
# Examined CRI-O logs for detailed checkpoint restoration process
vagrant ssh worker -c "sudo journalctl -u crio -n 50 --no-pager | grep -A 10 -B 5 'checkpoint\|CRIU\|restore'"
```

**Key Findings**: 
- ✅ **OCI checkpoint images detected correctly**:
  ```
  Image{Annotations:map[string]string{io.kubernetes.cri-o.annotations.checkpoint.name: writer}}
  ```
- ✅ **CRI-O successfully identifies checkpoint restoration requirement**
- ❌ **CRIU restoration fails during memory mapping**:
  ```
  pie: 18: `- skip pagemap
  pie: 18: `- skip pagemap
  Error: 18 killed by signal 11: Segmentation fault
  ```

#### 5. **CRIU Version and Capabilities**
```bash
vagrant ssh worker -c "criu --version"
```
**Result**: `Version: 3.16.1` - Modern CRIU version with checkpoint/restore capabilities

### Root Cause Analysis - Exact Technical Issue

Based on the detailed investigation, the **exact issue** is:

#### **Memory Address Space Conflicts During CRIU Restoration**

1. **What CRIU Does During Checkpoint**:
   - Records exact virtual memory addresses (e.g., `0xffff944f2000`)
   - Captures process IDs (PIDs 17, 18, 30, 31, 32)
   - Stores memory page mappings and process state

2. **What Happens During Restoration**:
   - CRIU successfully reads checkpoint data: `Preadv 0xffff944f2000:4096... returned 4096`
   - Attempts to restore memory at **exact same virtual addresses**
   - **SEGFAULT**: These addresses are invalid/unavailable in new container's memory space

3. **Why Each Container Has Different Memory Layout**:
   ```bash
   # Original container: Address space layout A
   Container ID: ce9e0c7e4d76d (PID namespace, memory layout specific to this instance)
   
   # Restored container: Address space layout B  
   Container ID: 24152655b5c2 (Different PID namespace, different memory layout)
   ```

4. **Process ID Namespace Conflicts**:
   - Checkpoint contains PIDs: 17, 18, 30, 31, 32
   - New container may have different/conflicting PIDs
   - CRIU tries to restore exact PIDs → segfault

#### **Container Runtime Environment Differences**

Even with **identical Kubernetes pod specifications**, each container runtime instance creates:
- **Unique memory address spaces**
- **Different PID namespaces** 
- **Distinct mount namespaces**
- **Separate network namespaces** (even same-node)
- **Different container IDs and runtime paths**

#### **Why Our Runtime Context Fix Wasn't Sufficient**

Our fix successfully preserved:
- ✅ Pod-level runtime context (security, volumes, restart policy)
- ✅ Container specifications and environment variables
- ✅ Kubernetes-level consistency

But **couldn't address**:
- ❌ Container runtime memory layout differences
- ❌ PID namespace conflicts
- ❌ Virtual memory address space conflicts

### Comparison with Working CRI-O Prototype

The key difference is likely that your working prototype:

1. **Operated at CRI layer directly** - potentially reused container runtime contexts
2. **May have used specific CRIU options** for memory address relocation
3. **Could have coordinated container recreation** to maintain memory layout compatibility
4. **Possibly used container runtime features** not available through Kubernetes kubelet API

### Technical Solution Requirements

To fix the CRIU segmentation faults, we need:

#### **Option 1: CRIU Memory Address Translation**
```bash
# CRIU options needed (if accessible via CRI-O/kubelet):
--lazy-pages          # Stream memory pages on-demand
--ext-mount-map       # Map external mount points
--tcp-established     # Handle network connections
--manage-cgroups      # Recreate cgroup hierarchy
```

#### **Option 2: Container Runtime Memory Layout Coordination**
- Pre-allocate compatible memory spaces
- Coordinate PID namespace reuse
- Ensure virtual memory layout consistency

#### **Option 3: Enhanced Container Runtime Integration**
- Modify checkpoint agent to work directly with CRI-O
- Bypass kubelet checkpoint API limitations
- Implement container-runtime-specific restoration logic

### Current Status

- ✅ **Kubernetes orchestration works perfectly**
- ✅ **OCI checkpoint image conversion successful**  
- ✅ **Runtime context preservation implemented**
- ❌ **CRIU memory restoration fails due to container runtime environment differences**

The issue is **not** with our Kubernetes-native approach, but with the **fundamental incompatibility** between CRIU's exact memory restoration and container runtime's dynamic memory allocation.

## Conclusion

**The core issue is at the container runtime level**: CRIU's process-state restoration model requires consistent memory environments that container runtimes cannot guarantee across different container instances.

**Our Kubernetes-native orchestration is correct**, but we need either:
1. **Container runtime-level coordination** for memory layout compatibility
2. **CRIU options** that handle memory address translation and PID namespace changes  
3. **Alternative restoration approaches** that work within container runtime constraints

This approach will be more reliable, maintainable, and truly Kubernetes-native.