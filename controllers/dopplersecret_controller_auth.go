package controllers

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	secretsv1alpha1 "github.com/DopplerHQ/kubernetes-operator/api/v1alpha1"
	"github.com/DopplerHQ/kubernetes-operator/pkg/api"
	"github.com/DopplerHQ/kubernetes-operator/pkg/auth"
)

// Interface for different authentication methods
type AuthProvider interface {
	GetAPIContext(ctx context.Context) (*api.APIContext, error)
}

// Handle service token authentication
type ServiceTokenAuthProvider struct {
	client    client.Client
	tokenRef  *secretsv1alpha1.TokenSecretReference
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
		return nil, fmt.Errorf("unable to fetch token secret: %w", err)
	}

	serviceToken, ok := tokenSecret.Data["serviceToken"]
	if !ok {
		return nil, fmt.Errorf("token secret does not contain 'serviceToken' field")
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
	host         string
	verifyTLS    bool
}

func (o *OIDCAuthProvider) GetAPIContext(ctx context.Context) (*api.APIContext, error) {
	token, err := o.oidcProvider.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to get OIDC token: %w", err)
	}

	return &api.APIContext{
		Host:      o.host,
		APIKey:    token,
		VerifyTLS: o.verifyTLS,
	}, nil
}

// Determine which authentication provider to use
func (r *DopplerSecretReconciler) getAuthProvider(ctx context.Context, dopplerSecret *secretsv1alpha1.DopplerSecret) (AuthProvider, error) {
	// Ensure only one auth method is specced
	if dopplerSecret.Spec.OIDCAuth != nil && dopplerSecret.Spec.TokenSecretRef != nil {
		return nil, fmt.Errorf("cannot specify both 'oidcAuth' and 'tokenSecret' fields - use one or the other")
	}

	// Check for OIDC auth config
	if dopplerSecret.Spec.OIDCAuth != nil {
		return r.createOIDCAuthProvider(ctx, dopplerSecret)
	}

	// Check for TokenSecretRef field
	if dopplerSecret.Spec.TokenSecretRef != nil {
		return &ServiceTokenAuthProvider{
			client:    r.Client,
			tokenRef:  dopplerSecret.Spec.TokenSecretRef,
			namespace: dopplerSecret.Namespace,
			host:      dopplerSecret.Spec.Host,
			verifyTLS: dopplerSecret.Spec.VerifyTLS,
		}, nil
	}

	return nil, fmt.Errorf("no authentication method configured")
}

// Create an OIDC authentication provider
func (r *DopplerSecretReconciler) createOIDCAuthProvider(ctx context.Context, dopplerSecret *secretsv1alpha1.DopplerSecret) (AuthProvider, error) {
	oidcConfig := dopplerSecret.Spec.OIDCAuth

	// Create kubernetes clientset for TokenRequest API
	config, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("unable to get kubernetes config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("unable to create kubernetes clientset: %w", err)
	}

	// Determine service account namespace
	saNamespace := dopplerSecret.Namespace
	if oidcConfig.ServiceAccountRef.Namespace != "" {
		saNamespace = oidcConfig.ServiceAccountRef.Namespace
	}

	// Use configured TTL
	tokenTTL := time.Duration(*oidcConfig.ExpirationSeconds) * time.Second

	authConfig := auth.OIDCConfig{
		ServiceAccountName: oidcConfig.ServiceAccountRef.Name,
		Namespace:          saNamespace,
		Audiences:          oidcConfig.Audiences,
		IdentityID:         oidcConfig.IdentityID,
		TokenTTL:           tokenTTL,
	}

	oidcProvider := auth.NewOIDCAuthProvider(clientset, authConfig, dopplerSecret.Spec.Host, dopplerSecret.Spec.VerifyTLS)

	return &OIDCAuthProvider{
		oidcProvider: oidcProvider,
		host:         dopplerSecret.Spec.Host,
		verifyTLS:    dopplerSecret.Spec.VerifyTLS,
	}, nil
}
