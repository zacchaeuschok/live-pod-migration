# Checkpoint/Restore Setup

This document describes the checkpoint/restore configuration that has been automated in the Vagrant setup scripts.

## Root Cause Analysis

The original checkpoint failures were caused by the Ubuntu runc package being built **without CRIU support**. The error "configured runtime does not support checkpoint/restore" was accurate - the runtime literally didn't support it.

## Solution Implemented

### 1. CRIU-Enabled runc Build (`common.sh`)

The setup now automatically:
- Installs build dependencies (`build-essential`, `libseccomp-dev`, `pkg-config`)
- Downloads Go 1.22.2 for building runc (Ubuntu's Go 1.18 has compatibility issues)
- Clones runc v1.2.5 source code
- Builds runc with CRIU support using build tags: `seccomp apparmor selinux criu`
- Replaces the system runc (`/usr/sbin/runc`) with the CRIU-enabled version
- Restarts CRI-O to use the new runtime

### 2. ContainerCheckpoint Feature Gate (`master.sh`)

The master setup now:
- Adds `ContainerCheckpoint: true` to `/var/lib/kubelet/config.yaml`
- Restarts kubelet to enable the feature gate
- Verifies the feature is working

### 3. Verification Steps (`master.sh`)

The setup verifies:
- runc has checkpoint/restore commands available
- CRIU passes its functionality check (`criu check`)
- Checkpoint API endpoint is accessible via kubelet

## Configuration Details

### CRI-O Configuration
- `enable_criu_support = true` in `/etc/crio/crio.conf`
- Uses runc as `default_runtime` (not crun)
- Runtime path: `/usr/sbin/runc` (replaced with CRIU-enabled version)

### Kubelet Configuration
- ContainerCheckpoint feature gate enabled
- Container runtime endpoint: `unix:///var/run/crio/crio.sock`

### Runtime Verification
- runc version: 1.2.5 with CRIU support
- CRIU version: 3.16.1
- Kubernetes version: 1.30.14

## Testing

After setup, you can test checkpointing:

```bash
# Create a test pod
kubectl run test-pod --image=nginx --restart=Never

# Create a checkpoint
kubectl apply -f - <<EOF
apiVersion: lpm.my.domain/v1
kind: ContainerCheckpoint
metadata:
  name: test-checkpoint
spec:
  podName: test-pod
  containerName: nginx
EOF

# Check status
kubectl describe containercheckpoint test-checkpoint
```

The checkpoint should now succeed with `Phase: Completed` instead of failing with "configured runtime does not support checkpoint/restore".

## Architecture Support

This setup works on both:
- **AMD64** (x86_64) systems
- **ARM64** (aarch64) systems (tested on Apple M1 via Parallels)

The runc build process automatically detects the architecture and builds the appropriate binary.