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

	v1alpha1 "sigs.k8s.io/multi-network/apis/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// PodNetworkReconciler reconciles a PodNetwork object
type PodNetworkReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const podNetworkFinalizer = "network.multi-network.x-k8s.io/finalizer"
const provider = "podnetwork.example.com"

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *PodNetworkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, rerr error) {
	logger := log.FromContext(ctx).WithValues("podnetwork", req.Name)
	logger.Info("Reconciling PodNetwork")

	podNetwork := &v1alpha1.PodNetwork{}
	if err := r.Get(ctx, req.NamespacedName, podNetwork); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("PodNetwork resource not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get PodNetwork")
		return ctrl.Result{}, err
	}

	if podNetwork.Spec.Provider != provider {
		logger.Info("had %v, want %v", podNetwork.Spec.Provider, provider)
		logger.Info("PodNetwork from different provider. Ignoring.")
		return ctrl.Result{}, nil
	}

	// Handle deletion: if the object is being deleted
	if !podNetwork.ObjectMeta.DeletionTimestamp.IsZero() {
		logger.V(2).Info("Deleting PodNetwork")
		if err := r.reconcileDelete(ctx, podNetwork); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile delete: %w", err)
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(podNetwork, podNetworkFinalizer) {
		logger.Info("Adding finalizer for PodNetwork")
		if err := r.Update(ctx, podNetwork); err != nil {
			return ctrl.Result{}, fmt.Errorf("update PodNetwork: %w", err)
		}
	}

	readyCond := &metav1.Condition{
		Type: "Ready",
	}

	r.reconcileNormal(ctx, podNetwork, readyCond)

	readyCond.ObservedGeneration = podNetwork.GetGeneration()

	if meta.SetStatusCondition(&podNetwork.Status.Conditions, *readyCond) {
		if err := r.Status().Update(ctx, podNetwork); err != nil {
			return ctrl.Result{}, fmt.Errorf("update PodNetwork status: %w", err)
		}
	}

	logger.Info("Successfully reconciled PodNetwork")
	return ctrl.Result{}, rerr
}

func (r *PodNetworkReconciler) reconcileDelete(ctx context.Context, podNetwork *v1alpha1.PodNetwork) error {
	logger := log.FromContext(ctx)
	if controllerutil.RemoveFinalizer(podNetwork, podNetworkFinalizer) {
		logger.Info("Finalization tasks completed. Removing finalizer.")
		if err := r.Update(ctx, podNetwork); err != nil {
			return err
		}
	}
	return nil
}

func (r *PodNetworkReconciler) reconcileNormal(ctx context.Context, podNetwork *v1alpha1.PodNetwork, readyCond *metav1.Condition) error {
	logger := log.FromContext(ctx)

	// Main reconciliation logic
	if !podNetwork.Spec.Enabled {
		logger.Info("PodNetwork is disabled")
		readyCond.Status = metav1.ConditionFalse
		readyCond.Reason = "Disabled"
		readyCond.Message = "PodNetwork is administratively disabled"
	} else {
		logger.Info("PodNetwork is enabled")
		readyCond.Status = metav1.ConditionTrue
		readyCond.Reason = "Ready"
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodNetworkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Uncomment the following line adding a pointer to an instance of the controlled resource as an argument
		For(&v1alpha1.PodNetwork{}).
		Named("podnetwork").
		Complete(r)
}
