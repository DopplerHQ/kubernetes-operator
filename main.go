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

package main

import (
	"context"
	"flag"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
	"github.com/DopplerHQ/kubernetes-operator/controllers"
	"github.com/DopplerHQ/kubernetes-operator/pkg/version"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(secretsv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

// managedSecretCacheRunnable makes a standalone cache satisfy controller-runtime's
// hasCache interface. Without GetCache the manager files a bare cache under its "Others"
// runnable group with a no-op readiness check, so it never waits for the informer to sync
// and never reports a cache that cannot sync. With it, the cache joins the Caches group
// and startup blocks on WaitForCacheSync like the manager's own cache does.
type managedSecretCacheRunnable struct {
	cache.Cache
}

func (c managedSecretCacheRunnable) GetCache() cache.Cache { return c.Cache }

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var oidcProviderCacheSize int
	var enableSecretCache bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.IntVar(&oidcProviderCacheSize, "oidc-provider-cache-size", 2<<13, "Size of the OIDC provider cache. Set to 0 to disable caching.")
	flag.BoolVar(&enableSecretCache, "enable-secret-cache", true,
		"Cache the Kubernetes Secrets this operator manages, selected by the "+
			"secrets.doppler.com/subtype=dopplerSecret label stamped on every managed secret. "+
			"Secrets the operator does not manage are never cached. Set to false to disable "+
			"Secret caching entirely, so every Secret read goes to the API server.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("controllers").WithName("DopplerSecret")

	controllers.InitializeOIDCCache(log, oidcProviderCacheSize)

	// Shared with the managed secret cache built below.
	restCfg := ctrl.GetConfigOrDie()

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme: scheme,
		// The manager's client never caches Secrets. A single cached Secret read starts
		// a cluster-wide informer holding every Secret in the cluster, however few the
		// operator uses. Managed secrets are served by the filtered cache below instead;
		// token secrets are read live.
		Client: client.Options{
			Cache: &client.CacheOptions{
				DisableFor: []client.Object{&corev1.Secret{}},
			},
		},
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "f39fa519.doppler.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// A cache holding only our own managed secrets. Reads of them stay cached — which is
	// what keeps reconciles cheap — while the cluster's other Secrets are never fetched.
	// Token secrets are user-created and cannot carry our label, so they stay live reads.
	var managedSecretReader client.Reader
	if enableSecretCache {
		setupLog.Info("Caching managed secrets only",
			"selector", controllers.ManagedSecretLabelKey+"="+controllers.ManagedSecretLabelValue)
		managedSecretCache, cacheErr := cache.New(restCfg, cache.Options{
			Scheme: scheme,
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Secret{}: {
					Label:     labels.SelectorFromSet(labels.Set{controllers.ManagedSecretLabelKey: controllers.ManagedSecretLabelValue}),
					Transform: cache.TransformStripManagedFields(),
				},
			},
			// Guards against this cache being reused for another type, which would
			// quietly start an unfiltered informer for it.
			ReaderFailOnMissingInformer: true,
		})
		if cacheErr != nil {
			setupLog.Error(cacheErr, "unable to build managed secret cache")
			os.Exit(1)
		}
		if _, err := managedSecretCache.GetInformer(context.Background(), &corev1.Secret{}); err != nil {
			setupLog.Error(err, "unable to start managed secret informer")
			os.Exit(1)
		}
		if err := mgr.Add(managedSecretCacheRunnable{managedSecretCache}); err != nil {
			setupLog.Error(err, "unable to add managed secret cache to manager")
			os.Exit(1)
		}
		managedSecretReader = managedSecretCache
	} else {
		setupLog.Info("Secret caching disabled; all Secret reads go to the API server")
	}

	if err = (&controllers.DopplerSecretReconciler{
		Client:              mgr.GetClient(),
		Log:                 log,
		Scheme:              mgr.GetScheme(),
		ManagedSecretReader: managedSecretReader,
		APIReader:           mgr.GetAPIReader(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "DopplerSecret")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager", "controllerVersion", version.ControllerVersion)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
