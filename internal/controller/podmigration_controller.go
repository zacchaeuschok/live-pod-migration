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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"time"

	lpmv1 "my.domain/guestbook/api/v1"
)

// PodMigrationReconciler reconciles a PodMigration object
type PodMigrationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=lpm.my.domain,resources=podmigrations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=lpm.my.domain,resources=podmigrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=lpm.my.domain,resources=podmigrations/finalizers,verbs=update
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
			podMigration.Status.Phase = lpmv1.MigrationPhaseRestoring
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

func (r *PodMigrationReconciler) handleRestoringPhase(ctx context.Context, podMigration *lpmv1.PodMigration) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("Handling Restoring phase for PodMigration", "name", podMigration.Name)

	checkpointName := podMigration.Name

	var podCheckpoint lpmv1.PodCheckpoint
	err := r.Get(ctx, client.ObjectKey{Namespace: podMigration.Namespace, Name: checkpointName}, &podCheckpoint)
	if apierrors.IsNotFound(err) {
		// Create a new PodCheckpoint if it doesn't exist
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

		// Update status to reference the newly created checkpoint
		podMigration.Status.PodCheckpointRef = &corev1.LocalObjectReference{Name: podCheckpoint.Name}
		if err := r.Status().Update(ctx, podMigration); err != nil {
			return ctrl.Result{}, err
		}

		logger.Info("PodCheckpoint created", "name", podCheckpoint.Name)
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}

	// At this point, the PodCheckpoint exists; react based on its phase
	switch podCheckpoint.Status.Phase {
	case lpmv1.PodCheckpointPhaseSucceeded:
		return ctrl.Result{}, r.updatePhase(ctx, podMigration, lpmv1.MigrationPhaseRestoring, "Pod checkpoint succeeded")
	case lpmv1.PodCheckpointPhaseFailed:
		return ctrl.Result{}, r.updatePhase(ctx, podMigration, lpmv1.MigrationPhaseFailed, "Pod checkpoint failed: "+podCheckpoint.Status.Message)
	default:
		logger.Info("Waiting for PodCheckpoint to complete", "phase", podCheckpoint.Status.Phase)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
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
func (r *PodMigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&lpmv1.PodMigration{}).
		Named("podmigration").
		Complete(r)
}
