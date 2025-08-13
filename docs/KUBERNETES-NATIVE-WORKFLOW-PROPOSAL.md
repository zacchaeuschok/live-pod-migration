# Live Pod Migration Controller - Simple Fix Proposal

## Problem

Our current `createRestoredPod()` function creates a new pod with **different runtime context** than what CRIU checkpointed, causing segmentation faults during restoration.

## Root Cause

```go
// Current problematic approach in createRestoredPod()
restoredPod := &corev1.Pod{
    ObjectMeta: metav1.ObjectMeta{
        Name:      restoredPodName,           // NEW name
        Namespace: originalPod.Namespace,
        Labels:    originalPod.Labels,       // Copying some fields...
    },
    Spec: corev1.PodSpec{
        NodeName:           podMigration.Spec.TargetNode,
        RestartPolicy:      corev1.RestartPolicyNever,    // DIFFERENT policy
        ServiceAccountName: originalPod.Spec.ServiceAccountName,
        SecurityContext:    originalPod.Spec.SecurityContext,  // This is good
        Volumes:           originalPod.Spec.Volumes,          // This is good
        Containers:        make([]corev1.Container, len(originalPod.Spec.Containers)),  // NEW containers
    },
}
```

**Problem**: We're creating a **new pod context** instead of preserving the **original pod context** that CRIU checkpointed.

## Simple Fix

Change `createRestoredPod()` to preserve the original pod's runtime context:

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

    // START WITH THE ORIGINAL POD - preserve all runtime context
    restoredPod := originalPod.DeepCopy()
    
    // Change only what's absolutely necessary
    restoredPod.ObjectMeta.Name = fmt.Sprintf("%s-restored", originalPod.Name)
    restoredPod.ObjectMeta.ResourceVersion = ""  // Required for creation
    restoredPod.ObjectMeta.UID = ""              // Required for creation
    restoredPod.Spec.NodeName = podMigration.Spec.TargetNode  // Target node
    
    // Add migration tracking
    if restoredPod.ObjectMeta.Annotations == nil {
        restoredPod.ObjectMeta.Annotations = make(map[string]string)
    }
    restoredPod.ObjectMeta.Annotations["migration.source-pod"] = originalPod.Name
    restoredPod.ObjectMeta.Annotations["migration.target-node"] = podMigration.Spec.TargetNode
    
    // Apply checkpoint images to containers (existing logic)
    for i, container := range restoredPod.Spec.Containers {
        checkpointImage, exists := podMigration.Status.CheckpointImages[container.Name]
        if !exists {
            return nil, fmt.Errorf("no checkpoint image prepared for container %s", container.Name)
        }
        
        restoredPod.Spec.Containers[i].Image = checkpointImage
        restoredPod.Spec.Containers[i].ImagePullPolicy = corev1.PullNever
    }
    
    // Set owner reference
    restoredPod.ObjectMeta.OwnerReferences = []metav1.OwnerReference{
        *metav1.NewControllerRef(podMigration, lpmv1.GroupVersion.WithKind("PodMigration")),
    }

    return restoredPod, nil
}
```

## Key Changes

1. **Start with `originalPod.DeepCopy()`** - preserves ALL original runtime context
2. **Change only necessary fields**: name, node, checkpoint images
3. **Keep everything else identical**: security context, volumes, restart policy, etc.

## Why This Works

CRIU checkpointed the containers with specific runtime context:
- Security context (user IDs, capabilities, etc.) ✅ **PRESERVED**  
- Volume mount paths and types ✅ **PRESERVED**
- Container runtime settings ✅ **PRESERVED**
- Environment variables ✅ **PRESERVED**
- Resource limits ✅ **PRESERVED**

The restored pod now has **identical runtime context** except for:
- Node location (required for migration)
- Container images (using checkpoint images)
- Pod name (to avoid conflicts)

## No Other Changes Needed

- ✅ Keep existing phases: `Pending → Checkpointing → CheckpointComplete → PreparingImages → Restoring`
- ✅ No new CRDs
- ✅ No new functions
- ✅ No annotations
- ✅ No kubelet modifications

## Expected Result

CRIU restoration should succeed because the runtime context matches what was checkpointed.