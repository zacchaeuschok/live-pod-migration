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
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	lpmv1 "my.domain/guestbook/api/v1"
)

// PodCheckpointReconciler reconciles a PodCheckpoint object
type PodCheckpointReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=lpm.my.domain,resources=podcheckpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=lpm.my.domain,resources=podcheckpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=lpm.my.domain,resources=podcheckpoints/finalizers,verbs=update
// +kubebuilder:rbac:groups=lpm.my.domain,resources=podcheckpointcontents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=lpm.my.domain,resources=podcheckpointcontents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=lpm.my.domain,resources=containercheckpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=lpm.my.domain,resources=containercheckpoints/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

func (r *PodCheckpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var podCheckpoint lpmv1.PodCheckpoint
	if err := r.Get(ctx, req.NamespacedName, &podCheckpoint); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if podCheckpoint.Status.Phase == "" {
		podCheckpoint.Status.Phase = lpmv1.PodCheckpointPhasePending
	}

	switch podCheckpoint.Status.Phase {
	case lpmv1.PodCheckpointPhasePending:
		return r.handlePendingPhase(ctx, &podCheckpoint)
	case lpmv1.PodCheckpointPhaseRunning:
		return r.handleCheckpointingPhase(ctx, &podCheckpoint)
	case lpmv1.PodCheckpointPhaseSucceeded, lpmv1.PodCheckpointPhaseFailed:
		return r.handleCompletedOrFailedPhase(ctx, &podCheckpoint)
	default:
		logger.Info("Unknown phase, nothing to do", "phase", podCheckpoint.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (r *PodCheckpointReconciler) handlePendingPhase(ctx context.Context, podCheckpoint *lpmv1.PodCheckpoint) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("Handling Pending phase for PodCheckpoint", "name", podCheckpoint.Name)

	// 1. Validate source Pod exists
	var srcPod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: podCheckpoint.Namespace, Name: *podCheckpoint.Spec.PodName}, &srcPod); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.updatePhase(ctx, podCheckpoint, lpmv1.PodCheckpointPhaseFailed, "source pod not found")
		}
		return ctrl.Result{}, err
	}

	// 2. Ensure Pod is running
	if srcPod.Status.Phase != corev1.PodRunning {
		return ctrl.Result{}, r.updatePhase(ctx, podCheckpoint, lpmv1.PodCheckpointPhaseFailed, "source pod not running")
	}

	// 3. Iterate containers and ensure ContainerCheckpoint objects
	createdAny := false
	for _, container := range srcPod.Spec.Containers {
		containerCheckpointName := podCheckpoint.Name + "-" + container.Name
		var containerCheckpoint lpmv1.ContainerCheckpoint
		err := r.Get(ctx, client.ObjectKey{Namespace: podCheckpoint.Namespace, Name: containerCheckpointName}, &containerCheckpoint)
		if apierrors.IsNotFound(err) {
			// create new ContainerCheckpoint
			containerCheckpoint = lpmv1.ContainerCheckpoint{
				ObjectMeta: metav1.ObjectMeta{
					Name:      containerCheckpointName,
					Namespace: podCheckpoint.Namespace,
					Labels: map[string]string{
						"podcheckpoint": podCheckpoint.Name,
					},
					OwnerReferences: []metav1.OwnerReference{
						*metav1.NewControllerRef(podCheckpoint, lpmv1.GroupVersion.WithKind("PodCheckpoint")),
					},
				},
				Spec: lpmv1.ContainerCheckpointSpec{
					PodName:       *podCheckpoint.Spec.PodName,
					ContainerName: container.Name,
				},
			}
			if err := r.Create(ctx, &containerCheckpoint); err != nil {
				return ctrl.Result{}, err
			}
			logger.Info("Created ContainerCheckpoint", "name", containerCheckpointName)
			createdAny = true
		} else if err != nil {
			return ctrl.Result{}, err
		}
	}

	// 4. Promote phase to Running and update status
	podCheckpoint.Status.Phase = lpmv1.PodCheckpointPhaseRunning
	podCheckpoint.Status.Message = "checkpointing containers"
	if err := r.Status().Update(ctx, podCheckpoint); err != nil {
		return ctrl.Result{}, err
	}

	if createdAny {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func (r *PodCheckpointReconciler) handleCheckpointingPhase(ctx context.Context, podCheckpoint *lpmv1.PodCheckpoint) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("Handling Running phase for PodCheckpoint", "name", podCheckpoint.Name)

	// 1. List all ContainerCheckpoint objects owned by this PodCheckpoint
	var containerCheckpointList lpmv1.ContainerCheckpointList
	if err := r.List(ctx, &containerCheckpointList, client.InNamespace(podCheckpoint.Namespace), client.MatchingLabels{"podcheckpoint": podCheckpoint.Name}); err != nil {
		return ctrl.Result{}, err
	}

	// If none found, defensively call pending handler to (re)create children
	if len(containerCheckpointList.Items) == 0 {
		logger.Info("No ContainerCheckpoints found; re-invoking pending handler")
		return r.handlePendingPhase(ctx, podCheckpoint)
	}

	// 2. Evaluate child states
	allDone := true
	allSucceeded := true
	var containerContentNames []corev1.LocalObjectReference

	for _, containerCheckpoint := range containerCheckpointList.Items {
		switch containerCheckpoint.Status.Phase {
		case lpmv1.ContainerCheckpointPhaseSucceeded:
			if containerCheckpoint.Status.BoundContentName != "" {
				containerContentNames = append(containerContentNames, corev1.LocalObjectReference{Name: containerCheckpoint.Status.BoundContentName})
			} else {
				allDone = false // succeeded but no content, wait
			}
		case lpmv1.ContainerCheckpointPhaseFailed:
			allDone = true  // we can finish evaluation now
			allSucceeded = false
		default: // Pending or Running or empty phase
			allDone = false
		}
	}

	// Wait if not all done yet
	if !allDone {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// If any child failed, mark failed
	if !allSucceeded {
		return ctrl.Result{}, r.updatePhase(ctx, podCheckpoint, lpmv1.PodCheckpointPhaseFailed, "one or more containers failed (see ContainerCheckpoint statuses)")
	}

	// 3. Ensure PodCheckpointContent exists & bound
	if podCheckpoint.Status.BoundContentName == "" {
		podCheckpointContentName := podCheckpoint.Name

		var podCheckpointContent lpmv1.PodCheckpointContent
		err := r.Get(ctx, client.ObjectKey{Name: podCheckpointContentName, Namespace: podCheckpoint.Namespace}, &podCheckpointContent)
		if apierrors.IsNotFound(err) {
			// build new content
			podCheckpointContent = lpmv1.PodCheckpointContent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podCheckpointContentName,
					Namespace: podCheckpoint.Namespace,
					OwnerReferences: []metav1.OwnerReference{
						*metav1.NewControllerRef(podCheckpoint, lpmv1.GroupVersion.WithKind("PodCheckpoint")),
					},
				},
				Spec: lpmv1.PodCheckpointContentSpec{
					PodCheckpointRef: corev1.ObjectReference{
						Namespace: podCheckpoint.Namespace,
						Name:      podCheckpoint.Name,
					},
					PodNamespace: podCheckpoint.Namespace,
					PodName:      *podCheckpoint.Spec.PodName,
					ContainerContents: containerContentNames,
				},
			}
			if err := r.Create(ctx, &podCheckpointContent); err != nil {
				return ctrl.Result{}, err
			}
			// Requeue soon to wait for status defaulting
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		} else if err != nil {
			return ctrl.Result{}, err
		}

		// record binding
		podCheckpoint.Status.BoundContentName = podCheckpointContent.Name
		if err := r.Status().Update(ctx, podCheckpoint); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// 4. Confirm PodCheckpointContent ready
	var boundContent lpmv1.PodCheckpointContent
	if err := r.Get(ctx, client.ObjectKey{Name: podCheckpoint.Status.BoundContentName, Namespace: podCheckpoint.Namespace}, &boundContent); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	if !boundContent.Status.Ready {
		// For PoC, mark ready now
		boundContent.Status.Ready = true
		boundContent.Status.CreationTime = &metav1.Time{Time: time.Now()}
		if err := r.Status().Update(ctx, &boundContent); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 5. Mark PodCheckpoint complete
	podCheckpoint.Status.Phase = lpmv1.PodCheckpointPhaseSucceeded
	podCheckpoint.Status.Message = "checkpoint complete"
	podCheckpoint.Status.Ready = true
	podCheckpoint.Status.CompletionTime = &metav1.Time{Time: time.Now()}
	if err := r.Status().Update(ctx, podCheckpoint); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *PodCheckpointReconciler) handleCompletedOrFailedPhase(ctx context.Context, podCheckpoint *lpmv1.PodCheckpoint) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("Handling Completed or Failed phase for PodCheckpoint", "name", podCheckpoint.Name)

	// No further action needed, just log the final state
	if podCheckpoint.Status.Phase == lpmv1.PodCheckpointPhaseSucceeded {
		logger.Info("PodCheckpoint completed successfully", "name", podCheckpoint.Name)
	} else {
		logger.Info("PodCheckpoint failed", "name", podCheckpoint.Name, "message", podCheckpoint.Status.Message)
	}

	return ctrl.Result{}, nil
}

func (r *PodCheckpointReconciler) updatePhase(ctx context.Context, podCheckpoint *lpmv1.PodCheckpoint, phase lpmv1.PodCheckpointPhase, message string) error {
	podCheckpoint.Status.Phase = phase
	podCheckpoint.Status.Message = message
	return r.Status().Update(ctx, podCheckpoint)
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodCheckpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&lpmv1.PodCheckpoint{}).
		Named("podcheckpoint").
		Complete(r)
}
