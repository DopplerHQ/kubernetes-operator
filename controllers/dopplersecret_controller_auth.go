package controllers

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
	"github.com/DopplerHQ/kubernetes-operator/pkg/api"
	"github.com/DopplerHQ/kubernetes-operator/pkg/auth"
	"github.com/DopplerHQ/kubernetes-operator/pkg/cache"
)

var (
	oidcProviderCache *cache.Cache[*auth.OIDCAuthProvider]
)

func InitializeOIDCCache(log logr.Logger, cacheSize int) {
	oidcProviderCache = cache.New(cacheSize, func(provider *auth.OIDCAuthProvider) {
		log.Info("Evicting OIDC provider from cache",
			"namespace", provider.Namespace,
			"identity", provider.Identity)
	})
}

// Interface for different authentication methods
type AuthProvider interface {
	GetAPIContext(ctx context.Context) (*api.APIContext, error)
}

// Handle service token authentication
type ServiceTokenAuthProvider struct {
	client    client.Client
	tokenRef  secretsv1alpha1.TokenSecretReference
	namespace string
	host      string
	verifyTLS bool
}

func (s *ServiceTokenAuthProvider) GetAPIContext(ctx context.Context) (*api.APIContext, error) {
	tokenSecret := corev1.Secret{}
	tokenNamespace := s.namespace
	if s.tokenRef.Namespace != "" {
		tokenNamespace = s.tokenRef.Namespace
	}

	err := s.client.Get(ctx, types.NamespacedName{
		Name:      s.tokenRef.Name,
		Namespace: tokenNamespace,
	}, &tokenSecret)
	if err != nil {
		return nil, fmt.Errorf("Unable to fetch token secret: %w", err)
	}

	serviceToken, ok := tokenSecret.Data["serviceToken"]
	if !ok {
		return nil, fmt.Errorf("Token secret does not contain 'serviceToken' field")
	}

	return &api.APIContext{
		Host:      s.host,
		APIKey:    string(serviceToken),
		VerifyTLS: s.verifyTLS,
	}, nil
}

// Handle OIDC authentication
type OIDCAuthProvider struct {
	oidcProvider *auth.OIDCAuthProvider
	cacheKey     cache.Key
}

func (o *OIDCAuthProvider) GetAPIContext(ctx context.Context) (*api.APIContext, error) {
	token, err := o.oidcProvider.GetToken(ctx)
	if err != nil {
		// On error, remove from cache to force retry
		oidcProviderCache.Remove(o.cacheKey)
		return nil, fmt.Errorf("Unable to get OIDC token: %w", err)
	}

	return &api.APIContext{
		Host:      o.oidcProvider.Host,
		APIKey:    token,
		VerifyTLS: o.oidcProvider.VerifyTLS,
	}, nil
}

// Determine which authentication provider to use
func (r *DopplerSecretReconciler) getAuthProvider(ctx context.Context, dopplerSecret *secretsv1alpha1.DopplerSecret) (AuthProvider, error) {
	// Use OIDC authentication with identity from spec
	if dopplerSecret.Spec.Identity != "" {
		return r.createOIDCAuthProvider(dopplerSecret, dopplerSecret.Spec.Identity, nil)
	}

	// Check what the token secret contains to determine auth type
	tokenSecret := corev1.Secret{}
	tokenNamespace := dopplerSecret.Namespace
	if dopplerSecret.Spec.TokenSecretRef.Namespace != "" {
		tokenNamespace = dopplerSecret.Spec.TokenSecretRef.Namespace
	}

	err := r.Client.Get(ctx, types.NamespacedName{
		Name:      dopplerSecret.Spec.TokenSecretRef.Name,
		Namespace: tokenNamespace,
	}, &tokenSecret)
	if err != nil {
		return nil, fmt.Errorf("Unable to fetch token secret: %w", err)
	}

	// Check what authentication fields exist
	_, hasServiceToken := tokenSecret.Data["serviceToken"]
	tokenSecretIdentity, hasTokenSecretIdentity := tokenSecret.Data["identity"]

	// Ensure mutual exclusivity between auth methods
	if hasServiceToken && hasTokenSecretIdentity {
		return nil, fmt.Errorf("Token secret cannot contain both 'serviceToken' and 'identity' fields - use one or the other")
	}

	// Use OIDC authentication
	if hasTokenSecretIdentity {
		return r.createOIDCAuthProvider(dopplerSecret, string(tokenSecretIdentity), &tokenSecret)
	}

	// Use service token authentication
	if hasServiceToken {
		return &ServiceTokenAuthProvider{
			client:    r.Client,
			tokenRef:  dopplerSecret.Spec.TokenSecretRef,
			namespace: dopplerSecret.Namespace,
			host:      dopplerSecret.Spec.Host,
			verifyTLS: dopplerSecret.Spec.VerifyTLS,
		}, nil
	}

	return nil, fmt.Errorf("Token secret must contain either 'serviceToken' or 'identity' field")
}

// Create an OIDC authentication provider
func (r *DopplerSecretReconciler) createOIDCAuthProvider(dopplerSecret *secretsv1alpha1.DopplerSecret, identity string, tokenSecret *corev1.Secret) (AuthProvider, error) {
	operatorNamespace, err := GetOwnNamespace()
	if err != nil {
		return nil, fmt.Errorf("Unable to get operator namespace: %w", err)
	}

	audiences := []string{
		dopplerSecret.Spec.Host,
	}

	// Add audience for validation that the token was issued for this specific resource
	if tokenSecret != nil {
		// The dopplerTokenSecret audience allows us to validate that the token was issued
		// for this specific token secret
		audiences = append(audiences, fmt.Sprintf("dopplerTokenSecret:%s:%s",
			tokenSecret.Namespace,
			tokenSecret.Name))
	} else {
		// Identity is provided in the DopplerSecret, so we include a dopplerSecret audience
		audiences = append(audiences, fmt.Sprintf("dopplerSecret:%s:%s",
			dopplerSecret.Namespace,
			dopplerSecret.Name))
	}

	// If the identity is a UUID, we add it as an additional audience to allow for a
	// cryptographic binding of the JWT to its intended identity
	uuidRegex := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	if uuidRegex.MatchString(strings.ToLower(identity)) {
		audiences = append(audiences, identity)
	}

	// Get expiration seconds from spec or secret, default to 600
	expirationSeconds := int64(600)

	// Ensure mutual exclusivity for expirationSeconds
	if dopplerSecret.Spec.ExpirationSeconds > 0 && tokenSecret != nil {
		if _, ok := tokenSecret.Data["expirationSeconds"]; ok {
			return nil, fmt.Errorf("expirationSeconds specified in both DopplerSecret spec and tokenSecret - use one or the other")
		}
	}

	if dopplerSecret.Spec.ExpirationSeconds > 0 {
		expirationSeconds = dopplerSecret.Spec.ExpirationSeconds
	} else if tokenSecret != nil {
		if expSecondsData, ok := tokenSecret.Data["expirationSeconds"]; ok {
			if _, err := fmt.Sscanf(string(expSecondsData), "%d", &expirationSeconds); err != nil {
				r.Log.Info("Invalid expirationSeconds in token secret, using default",
					"value", string(expSecondsData),
					"default", expirationSeconds)
			}
		}
	}

	cacheKey := cache.Key{
		Identity:  identity,
		Audiences: strings.Join(audiences, ","),
	}

	var oidcProvider *auth.OIDCAuthProvider

	if cachedProvider, found := oidcProviderCache.Get(cacheKey); found {
		oidcProvider = cachedProvider
		r.Log.Info("Using cached OIDC provider",
			"namespace", dopplerSecret.Namespace,
			"name", dopplerSecret.Name,
			"cacheKey", cacheKey)
	}

	if oidcProvider == nil {
		r.Log.Info("Creating new OIDC provider",
			"namespace", dopplerSecret.Namespace,
			"name", dopplerSecret.Name,
			"identity", identity)

		config, err := ctrl.GetConfig()
		if err != nil {
			return nil, fmt.Errorf("Unable to get kubernetes config: %w", err)
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			return nil, fmt.Errorf("Unable to create kubernetes clientset: %w", err)
		}

		oidcProvider = &auth.OIDCAuthProvider{
			KubeClient:        clientset,
			Namespace:         operatorNamespace,
			Audiences:         audiences,
			Host:              dopplerSecret.Spec.Host,
			Identity:          identity,
			VerifyTLS:         dopplerSecret.Spec.VerifyTLS, // Defaults to true via kubebuilder annotation in CRD
			ExpirationSeconds: expirationSeconds,
		}

		// Add to cache
		oidcProviderCache.Add(cacheKey, oidcProvider)
		r.Log.Info("Added OIDC provider to cache",
			"namespace", dopplerSecret.Namespace,
			"name", dopplerSecret.Name,
			"cacheKey", cacheKey)
	}

	return &OIDCAuthProvider{
		oidcProvider: oidcProvider,
		cacheKey:     cacheKey,
	}, nil
}
