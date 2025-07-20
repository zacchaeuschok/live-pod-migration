/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"my.domain/guestbook/internal/agent"
	lpmv1 "my.domain/guestbook/api/v1"
)

// PodMigrationReconciler reconciles a PodMigration object
type PodMigrationReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	AgentClient *agent.Client
}

// +kubebuilder:rbac:groups=lpm.my.domain,resources=podmigrations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=lpm.my.domain,resources=podmigrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=lpm.my.domain,resources=podmigrations/finalizers,verbs=update
// +kubebuilder:rbac:groups=lpm.my.domain,resources=podcheckpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=lpm.my.domain,resources=podcheckpointcontents,verbs=get;list;watch
// +kubebuilder:rbac:groups=lpm.my.domain,resources=containercheckpointcontents,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch

func (r *PodMigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var podMigration lpmv1.PodMigration
	if err := r.Get(ctx, req.NamespacedName, &podMigration); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if podMigration.Status.Phase == "" {
		podMigration.Status.Phase = lpmv1.MigrationPhasePending
	}

	switch podMigration.Status.Phase {
	case lpmv1.MigrationPhasePending:
		return r.handlePendingPhase(ctx, &podMigration)
	case lpmv1.MigrationPhaseCheckpointing:
		return r.handleCheckpointingPhase(ctx, &podMigration)
	case lpmv1.MigrationPhaseCheckpointComplete:
		return r.handleCheckpointCompletePhase(ctx, &podMigration)
	case lpmv1.MigrationPhaseRestoring:
		return r.handleRestoringPhase(ctx, &podMigration)
	case lpmv1.MigrationPhaseSucceeded, lpmv1.MigrationPhaseFailed:
		return r.handleCompletedOrFailedPhase(ctx, &podMigration)
	default:
		logger.Info("Unknown phase, nothing to do", "phase", podMigration.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (r *PodMigrationReconciler) handlePendingPhase(ctx context.Context, podMigration *lpmv1.PodMigration) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("Handling Pending phase for PodMigration", "name", podMigration.Name)

	// 1. Validate source Pod exists
	var srcPod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: podMigration.Namespace, Name: podMigration.Spec.PodName}, &srcPod); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.updatePhase(ctx, podMigration, lpmv1.MigrationPhaseFailed, "source pod not found")
		}
		return ctrl.Result{}, err
	}

	// 2. Validate source Pod running
	if srcPod.Status.Phase != corev1.PodRunning {
		return ctrl.Result{}, r.updatePhase(ctx, podMigration, lpmv1.MigrationPhaseFailed, "source pod not running")
	}

	// 3. If target node requested, validate it exists
	if podMigration.Spec.TargetNode != "" {
		var node corev1.Node
		if err := r.Get(ctx, client.ObjectKey{Name: podMigration.Spec.TargetNode}, &node); err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{}, r.updatePhase(ctx, podMigration, lpmv1.MigrationPhaseFailed, "target node not found")
			}
			return ctrl.Result{}, err
		}
	}

	// 4/5. Ensure PodCheckpoint exists and update status accordingly
	checkpointName := podMigration.Name
	var podCheckpoint lpmv1.PodCheckpoint
	err := r.Get(ctx, client.ObjectKey{Namespace: podMigration.Namespace, Name: checkpointName}, &podCheckpoint)

	if apierrors.IsNotFound(err) {
		// Create new checkpoint
		podCheckpoint = lpmv1.PodCheckpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name:      checkpointName,
				Namespace: podMigration.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(podMigration, lpmv1.GroupVersion.WithKind("PodMigration")),
				},
			},
			Spec: lpmv1.PodCheckpointSpec{
				PodName: &podMigration.Spec.PodName,
			},
		}
		if err := r.Create(ctx, &podCheckpoint); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("PodCheckpoint created from Pending phase", "name", podCheckpoint.Name)

		podMigration.Status.PodCheckpointRef = &corev1.LocalObjectReference{Name: checkpointName}
		podMigration.Status.Phase = lpmv1.MigrationPhaseCheckpointing
		podMigration.Status.Message = "checkpoint requested"
		if err := r.Status().Update(ctx, podMigration); err != nil {
			return ctrl.Result{}, err
		}
		// requeue soon to start monitoring
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}

	// checkpoint already exists
	if podMigration.Status.PodCheckpointRef == nil {
		podMigration.Status.PodCheckpointRef = &corev1.LocalObjectReference{Name: podCheckpoint.Name}
	}
	podMigration.Status.Phase = lpmv1.MigrationPhaseCheckpointing
	podMigration.Status.Message = "checkpoint in progress"
	if err := r.Status().Update(ctx, podMigration); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *PodMigrationReconciler) handleCheckpointingPhase(ctx context.Context, podMigration *lpmv1.PodMigration) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling Checkpointing phase for PodMigration", "name", podMigration.Name)

	// Determine PodCheckpoint name: status ref if set, else fall back to migration name
	podCheckpointName := podMigration.Name
	if podMigration.Status.PodCheckpointRef != nil && podMigration.Status.PodCheckpointRef.Name != "" {
		podCheckpointName = podMigration.Status.PodCheckpointRef.Name
	}

	var podCheckpoint lpmv1.PodCheckpoint
	err := r.Get(ctx, client.ObjectKey{Namespace: podMigration.Namespace, Name: podCheckpointName}, &podCheckpoint)

	if apierrors.IsNotFound(err) {
		// Re-create checkpoint request
		podCheckpoint = lpmv1.PodCheckpoint{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podCheckpointName,
				Namespace: podMigration.Namespace,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(podMigration, lpmv1.GroupVersion.WithKind("PodMigration")),
				},
			},
			Spec: lpmv1.PodCheckpointSpec{
				PodName: &podMigration.Spec.PodName,
			},
		}
		if err := r.Create(ctx, &podCheckpoint); err != nil {
			return ctrl.Result{}, err
		}
		if podMigration.Status.PodCheckpointRef == nil {
			podMigration.Status.PodCheckpointRef = &corev1.LocalObjectReference{Name: podCheckpointName}
			if err := r.Status().Update(ctx, podMigration); err != nil {
				return ctrl.Result{}, err
			}
		}
		logger.Info("PodCheckpoint (re)created", "name", podCheckpointName)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}

	// Switch based on checkpoint status
	switch podCheckpoint.Status.Phase {
	case lpmv1.PodCheckpointPhaseFailed:
		return ctrl.Result{}, r.updatePhase(ctx, podMigration, lpmv1.MigrationPhaseFailed, "checkpoint failed: "+podCheckpoint.Status.Message)

	case lpmv1.PodCheckpointPhaseSucceeded:
		// Ensure checkpoint is truly ready
		if podCheckpoint.Status.Ready {
			podMigration.Status.Phase = lpmv1.MigrationPhaseCheckpointComplete
			podMigration.Status.Message = "checkpoint complete"
			if err := r.Status().Update(ctx, podMigration); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		// Not ready yet; fallthrough to default requeue
	}

	// Pending / Running / default
	logger.Info("Checkpoint in progress", "phase", podCheckpoint.Status.Phase)
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *PodMigrationReconciler) handleCheckpointCompletePhase(ctx context.Context, podMigration *lpmv1.PodMigration) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling CheckpointComplete phase for PodMigration", "name", podMigration.Name)

	// Create restored pod from checkpoint
	restoredPod, err := r.createRestoredPod(ctx, podMigration)
	if err != nil {
		return ctrl.Result{}, r.updatePhase(ctx, podMigration, lpmv1.MigrationPhaseFailed, fmt.Sprintf("failed to create restored pod: %v", err))
	}

	err = r.Create(ctx, restoredPod)
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			logger.Info("Restored pod already exists", "pod", restoredPod.Name)
		} else {
			return ctrl.Result{}, r.updatePhase(ctx, podMigration, lpmv1.MigrationPhaseFailed, fmt.Sprintf("failed to create restored pod: %v", err))
		}
	}

	// Update status with restored pod name and move to restoring phase
	podMigration.Status.RestoredPodName = restoredPod.Name
	podMigration.Status.Phase = lpmv1.MigrationPhaseRestoring
	podMigration.Status.Message = "restored pod created"
	if err := r.Status().Update(ctx, podMigration); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Restored pod created", "pod", restoredPod.Name)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *PodMigrationReconciler) handleRestoringPhase(ctx context.Context, podMigration *lpmv1.PodMigration) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling Restoring phase for PodMigration", "name", podMigration.Name)

	// Check restored pod status
	if podMigration.Status.RestoredPodName == "" {
		return ctrl.Result{}, r.updatePhase(ctx, podMigration, lpmv1.MigrationPhaseFailed, "no restored pod name in status")
	}

	var restoredPod corev1.Pod
	err := r.Get(ctx, client.ObjectKey{
		Name:      podMigration.Status.RestoredPodName,
		Namespace: podMigration.Namespace,
	}, &restoredPod)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.updatePhase(ctx, podMigration, lpmv1.MigrationPhaseFailed, "restored pod not found")
		}
		return ctrl.Result{}, err
	}

	// Check pod status
	switch restoredPod.Status.Phase {
	case corev1.PodRunning:
		// Delete original pod after successful restoration
		if err := r.deleteOriginalPod(ctx, podMigration); err != nil {
			logger.Error(err, "Failed to delete original pod, but migration succeeded")
		}
		return ctrl.Result{}, r.updatePhase(ctx, podMigration, lpmv1.MigrationPhaseSucceeded, "pod successfully restored and running")
	
	case corev1.PodFailed:
		return ctrl.Result{}, r.updatePhase(ctx, podMigration, lpmv1.MigrationPhaseFailed, "restored pod failed to start")
	
	case corev1.PodPending:
		logger.Info("Restored pod is pending", "pod", restoredPod.Name, "reason", restoredPod.Status.Reason)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	
	default:
		logger.Info("Restored pod in progress", "pod", restoredPod.Name, "phase", restoredPod.Status.Phase)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
}

func (r *PodMigrationReconciler) handleCompletedOrFailedPhase(ctx context.Context, podMigration *lpmv1.PodMigration) (ctrl.Result, error) {
	// Logic to handle the Succeeded or Failed phase
	// No further action needed for completed migrations
	return ctrl.Result{}, nil
}

func (r *PodMigrationReconciler) updatePhase(ctx context.Context, podMigration *lpmv1.PodMigration, phase lpmv1.PodMigrationPhase, message string) error {
	podMigration.Status.Phase = phase
	podMigration.Status.Message = message
	return r.Status().Update(ctx, podMigration)
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodMigrationReconciler) createRestoredPod(ctx context.Context, podMigration *lpmv1.PodMigration) (*corev1.Pod, error) {
	var originalPod corev1.Pod
	err := r.Get(ctx, client.ObjectKey{
		Namespace: podMigration.Namespace,
		Name:      podMigration.Spec.PodName,
	}, &originalPod)
	if err != nil {
		return nil, fmt.Errorf("failed to get original pod: %w", err)
	}

	checkpointContent, err := r.getCheckpointContent(ctx, podMigration)
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoint content: %w", err)
	}

	restoredPodName := fmt.Sprintf("%s-restored", originalPod.Name)

	restoredPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restoredPodName,
			Namespace: originalPod.Namespace,
			Labels:    originalPod.Labels,
			Annotations: map[string]string{
				"migration.source-pod":       originalPod.Name,
				"migration.target-node":      podMigration.Spec.TargetNode,
				"migration.checkpoint-source": checkpointContent.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(podMigration, lpmv1.GroupVersion.WithKind("PodMigration")),
			},
		},
		Spec: corev1.PodSpec{
			NodeName:           podMigration.Spec.TargetNode,
			RestartPolicy:      corev1.RestartPolicyNever,
			ServiceAccountName: originalPod.Spec.ServiceAccountName,
			SecurityContext:    originalPod.Spec.SecurityContext,
			Volumes:           originalPod.Spec.Volumes,
			Containers:        make([]corev1.Container, len(originalPod.Spec.Containers)),
		},
	}

	for i, container := range originalPod.Spec.Containers {
		restoredContainer := container.DeepCopy()
		
		checkpointPath := r.getCheckpointPathForContainer(ctx, checkpointContent, container.Name)
		if checkpointPath == "" {
			return nil, fmt.Errorf("no checkpoint found for container %s", container.Name)
		}
		
		// Use checkpoint file path directly for CRI-O auto-restoration
		// CRI-O automatically detects checkpoint files when container.image is a file path
		var checkpointFilePath string
		if strings.HasPrefix(checkpointPath, "shared://") {
			// Convert shared:// URI to local file path
			filename := strings.TrimPrefix(checkpointPath, "shared://")
			checkpointFilePath = filepath.Join("/mnt/checkpoints", filename)
		} else if strings.HasPrefix(checkpointPath, "file://") {
			// Use local file path directly
			checkpointFilePath = strings.TrimPrefix(checkpointPath, "file://")
		} else {
			return nil, fmt.Errorf("unsupported checkpoint path format: %s", checkpointPath)
		}
		
		restoredContainer.Image = checkpointFilePath
		restoredContainer.ImagePullPolicy = corev1.PullNever
		
		restoredPod.Spec.Containers[i] = *restoredContainer
	}

	return restoredPod, nil
}

func (r *PodMigrationReconciler) getCheckpointContent(ctx context.Context, podMigration *lpmv1.PodMigration) (*lpmv1.PodCheckpointContent, error) {
	if podMigration.Status.PodCheckpointRef == nil {
		return nil, fmt.Errorf("no checkpoint reference in migration status")
	}

	checkpointName := podMigration.Status.PodCheckpointRef.Name
	
	var podCheckpoint lpmv1.PodCheckpoint
	err := r.Get(ctx, client.ObjectKey{
		Namespace: podMigration.Namespace,
		Name:      checkpointName,
	}, &podCheckpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to get pod checkpoint: %w", err)
	}

	if podCheckpoint.Status.BoundContentName == "" {
		return nil, fmt.Errorf("checkpoint has no bound content")
	}

	var checkpointContent lpmv1.PodCheckpointContent
	err = r.Get(ctx, client.ObjectKey{
		Namespace: podMigration.Namespace,
		Name:      podCheckpoint.Status.BoundContentName,
	}, &checkpointContent)
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoint content: %w", err)
	}

	return &checkpointContent, nil
}

func (r *PodMigrationReconciler) getCheckpointPathForContainer(ctx context.Context, checkpointContent *lpmv1.PodCheckpointContent, containerName string) string {
	for _, containerContent := range checkpointContent.Spec.ContainerContents {
		var content lpmv1.ContainerCheckpointContent
		err := r.Get(ctx, client.ObjectKey{
			Name:      containerContent.Name,
			Namespace: checkpointContent.Namespace,
		}, &content)
		if err != nil {
			continue
		}
		
		if strings.Contains(content.Name, containerName) {
			return content.Spec.ArtifactURI
		}
	}
	return ""
}

func (r *PodMigrationReconciler) convertToOCIImage(ctx context.Context, checkpointURI, containerName, targetNode string) (string, error) {
	if !strings.HasPrefix(checkpointURI, "shared://") {
		return checkpointURI, nil
	}

	// Generate OCI image name
	filename := strings.TrimPrefix(checkpointURI, "shared://")
	imageName := fmt.Sprintf("localhost/checkpoint:%s", strings.TrimSuffix(filename, ".tar"))

	// Use agent to convert checkpoint to OCI image
	imageRef, err := r.AgentClient.ConvertCheckpointToImage(ctx, targetNode, checkpointURI, containerName, imageName)
	if err != nil {
		return "", fmt.Errorf("failed to convert checkpoint to OCI image: %w", err)
	}

	return imageRef, nil
}

func (r *PodMigrationReconciler) deleteOriginalPod(ctx context.Context, podMigration *lpmv1.PodMigration) error {
	var originalPod corev1.Pod
	err := r.Get(ctx, client.ObjectKey{
		Namespace: podMigration.Namespace,
		Name:      podMigration.Spec.PodName,
	}, &originalPod)
	
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get original pod for deletion: %w", err)
	}

	err = r.Delete(ctx, &originalPod)
	if err != nil {
		return fmt.Errorf("failed to delete original pod: %w", err)
	}

	return nil
}

func (r *PodMigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&lpmv1.PodMigration{}).
		Named("podmigration").
		Complete(r)
}
