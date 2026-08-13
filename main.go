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
	"k8s.io/apimachinery/pkg/selection"
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

// secretCacheRunnable makes a standalone cache satisfy controller-runtime's
// hasCache interface. Without GetCache the manager files a bare cache under its "Others"
// runnable group with a no-op readiness check, so it never waits for the informer to sync
// and never reports a cache that cannot sync. With it, the cache joins the Caches group
// and startup blocks on WaitForCacheSync like the manager's own cache does.
type secretCacheRunnable struct {
	cache.Cache
}

func (c secretCacheRunnable) GetCache() cache.Cache { return c.Cache }

// cachedSecretSelector matches the only Secrets the operator needs in memory: the ones it
// manages, and the token secrets it has been pointed at. Everything else in the cluster is
// never listed or watched.
var cachedSecretSelector = mustCachedSecretSelector()

func mustCachedSecretSelector() labels.Selector {
	requirement, err := labels.NewRequirement(
		controllers.ManagedSecretLabelKey,
		selection.In,
		[]string{controllers.ManagedSecretLabelValue, controllers.TokenSecretLabelValue},
	)
	if err != nil {
		// Only reachable if the label constants stop being valid label values.
		panic(err)
	}
	return labels.NewSelector().Add(*requirement)
}

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
		"Cache the Kubernetes Secrets this operator uses: the ones it manages, and any "+
			"token secrets labelled for it. Both are selected by the "+
			"secrets.doppler.com/subtype label. The operator stamps that label on the "+
			"managed secrets it creates. Token secrets belong to the user, so the operator "+
			"never writes to them: caching one means labelling it yourself with "+
			"secrets.doppler.com/subtype=dopplerToken, and an unlabelled token secret is "+
			"read from the API server on every reconcile. Every other Secret in the cluster "+
			"is never listed or watched. Set to false to disable Secret caching entirely, so "+
			"every Secret read goes to the API server.")
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

	// A cache holding only the secrets this operator touches: the ones it manages, and
	// the token secrets it has been pointed at. Reads of both stay cached, which is what
	// keeps reconciles cheap, while the cluster's other Secrets are never fetched.
	var cachedSecretReader client.Reader
	if enableSecretCache {
		setupLog.Info("Caching only the secrets this operator uses", "selector", cachedSecretSelector.String())
		secretCache, err := cache.New(restCfg, cache.Options{
			Scheme: scheme,
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Secret{}: {
					Label:     cachedSecretSelector,
					Transform: cache.TransformStripManagedFields(),
				},
			},
			// Guards against this cache being reused for another type, which would
			// quietly start an unfiltered informer for it.
			ReaderFailOnMissingInformer: true,
		})
		if err != nil {
			setupLog.Error(err, "unable to build secret cache")
			os.Exit(1)
		}
		if _, err := secretCache.GetInformer(context.Background(), &corev1.Secret{}); err != nil {
			setupLog.Error(err, "unable to start secret cache informer")
			os.Exit(1)
		}
		if err := mgr.Add(secretCacheRunnable{secretCache}); err != nil {
			setupLog.Error(err, "unable to add secret cache to manager")
			os.Exit(1)
		}
		cachedSecretReader = secretCache
	} else {
		setupLog.Info("Secret caching disabled; all Secret reads go to the API server")
	}

	if err = (&controllers.DopplerSecretReconciler{
		Client:             mgr.GetClient(),
		Log:                log,
		Scheme:             mgr.GetScheme(),
		CachedSecretReader: cachedSecretReader,
		APIReader:          mgr.GetAPIReader(),
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
