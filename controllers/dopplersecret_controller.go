/*
Copyright 2021.

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

package controllers

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
)

// DopplerSecretReconciler reconciles a DopplerSecret object
type DopplerSecretReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

const (
	defaultRequeueDuration = time.Minute
)

//+kubebuilder:rbac:groups=secrets.doppler.com,resources=dopplersecrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=secrets.doppler.com,resources=dopplersecrets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=secrets.doppler.com,resources=dopplersecrets/finalizers,verbs=update

//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;delete
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=list;patch
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;watch;list

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *DopplerSecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("dopplersecret", req.NamespacedName)

	log.Info("Reconciling dopplersecret", "secret", req.NamespacedName)

	dopplerSecret := secretsv1alpha1.DopplerSecret{}
	err := r.Client.Get(context.Background(), req.NamespacedName, &dopplerSecret)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("[-] dopplersecret not found, nothing to do", "secret", req.NamespacedName)
			return ctrl.Result{}, nil
		}
		log.Error(err, "Unable to fetch dopplersecret", "secret", req.NamespacedName)
		return ctrl.Result{
			RequeueAfter: defaultRequeueDuration,
		}, nil
	}

	requeueAfter := defaultRequeueDuration
	if dopplerSecret.Spec.ResyncSeconds != 0 {
		requeueAfter = time.Second * time.Duration(dopplerSecret.Spec.ResyncSeconds)
	}
	log.Info("Requeue duration set", "requeueAfter", requeueAfter)

	if dopplerSecret.GetDeletionTimestamp() != nil {
		log.Info("dopplersecret has been deleted, nothing to do", "secret", req.NamespacedName)
		return ctrl.Result{}, nil
	}

	err = r.UpdateSecret(dopplerSecret)
	if err != nil {
		log.Error(err, "Unable to update dopplersecret", "secret", req.NamespacedName)
		return ctrl.Result{
			RequeueAfter: requeueAfter,
		}, nil
	}

	err = r.ReconcileDeploymentsUsingSecret(dopplerSecret)
	if err != nil {
		log.Error(err, "Failed to update deployments", "secret", req.NamespacedName)
		return ctrl.Result{
			RequeueAfter: requeueAfter,
		}, nil
	}

	log.Info("Finished reconciliation", "secret", req.NamespacedName)
	return ctrl.Result{
		RequeueAfter: requeueAfter,
	}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DopplerSecretReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&secretsv1alpha1.DopplerSecret{}).
		Complete(r)
}
