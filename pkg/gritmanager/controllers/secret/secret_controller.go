// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package secret

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"time"

	"golang.org/x/time/rate"
	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	"knative.dev/pkg/webhook/certificates/resources"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kaito-project/grit/pkg/gritmanager/controllers/util"
)

const (
	girtManagerValidatingWebhookConfig = "grit-manager-validating-webhook-configuration"
	girtManagerMutatingWebhookConfig   = "grit-manager-mutating-webhook-configuration"
)

var (
	validatingWebhookConfigPredicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			vwc, ok := e.Object.(*admissionv1.ValidatingWebhookConfiguration)
			if !ok {
				return false
			}

			return vwc.Name == girtManagerValidatingWebhookConfig
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			vwc, ok := e.ObjectNew.(*admissionv1.ValidatingWebhookConfiguration)
			if !ok {
				return false
			}

			return vwc.Name == girtManagerValidatingWebhookConfig
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
	}

	mutatingWebhookConfigPredicate = predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			mwc, ok := e.Object.(*admissionv1.MutatingWebhookConfiguration)
			if !ok {
				return false
			}

			return mwc.Name == girtManagerMutatingWebhookConfig
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			mwc, ok := e.ObjectNew.(*admissionv1.MutatingWebhookConfiguration)
			if !ok {
				return false
			}

			return mwc.Name == girtManagerMutatingWebhookConfig
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
	}
)

type Controller struct {
	client.Client
	clock                   clock.Clock
	workingNamespace        string
	webhookServerSecretName string
	webhookServiceName      string
	expirationDuration      time.Duration
}

func NewController(clk clock.Clock, kubeClient client.Client, ns, secretName, serviceName string, expirationDuration time.Duration) *Controller {
	return &Controller{
		clock:                   clk,
		Client:                  kubeClient,
		workingNamespace:        ns,
		webhookServerSecretName: secretName,
		webhookServiceName:      serviceName,
		expirationDuration:      expirationDuration,
	}
}

func (c *Controller) Reconcile(ctx context.Context, secret *corev1.Secret) (reconcile.Result, error) {
	if secret.Namespace != c.workingNamespace || secret.Name != c.webhookServerSecretName {
		return reconcile.Result{}, nil
	}
	ctx = util.WithControllerName(ctx, "server.secret")
	if len(secret.Data[util.ServerCert]) != 0 &&
		len(secret.Data[util.ServerKey]) != 0 &&
		len(secret.Data[util.CACert]) != 0 {
		// if the certificate is valid for less than 15% of total validity , we will renew it.
		if shouldRenew, timeUntilNextCheck := shouldRenewCert(c.clock, secret.Data[util.ServerCert], secret.Data[util.ServerKey]); !shouldRenew {
			if err := c.updateWebhookConfigurations(ctx, secret.Data[util.CACert]); err != nil {
				return reconcile.Result{}, err
			}
			return reconcile.Result{RequeueAfter: timeUntilNextCheck}, nil
		}
	}

	updatedSecret := secret.DeepCopy()
	newSecret, err := generateSecret(ctx, c.webhookServiceName, c.webhookServerSecretName, c.workingNamespace, c.clock.Now().Add(c.expirationDuration))
	if err != nil {
		return reconcile.Result{}, err
	}
	updatedSecret.Data = newSecret.Data

	if err := c.Update(ctx, updatedSecret); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, c.updateWebhookConfigurations(ctx, updatedSecret.Data[util.CACert])
}

func generateSecret(ctx context.Context, serviceName, name, namespace string, notAfter time.Time) (*corev1.Secret, error) {
	serverKey, serverCert, caCert, err := resources.CreateCerts(ctx, serviceName, namespace, notAfter)
	if err != nil {
		return nil, err
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			util.ServerKey:  serverKey,
			util.ServerCert: serverCert,
			util.CACert:     caCert,
		},
	}, nil
}

func shouldRenewCert(clk clock.Clock, certPEMBlock, keyPEMBlock []byte) (bool, time.Duration) {
	cert, err := tls.X509KeyPair(certPEMBlock, keyPEMBlock)
	if err != nil {
		return true, 0
	}

	certData, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return true, 0
	}

	now := clk.Now()
	startTime := certData.NotBefore
	expiryTime := certData.NotAfter
	totalValidity := expiryTime.Sub(startTime)
	elapsed := now.Sub(startTime)

	if float64(elapsed)/float64(totalValidity) >= 0.85 {
		return true, 0
	}

	nextCheck := totalValidity / 20
	timeUntilNextCheck := expiryTime.Sub(now) - nextCheck
	if timeUntilNextCheck < time.Minute {
		timeUntilNextCheck = time.Minute
	}

	return false, timeUntilNextCheck
}

func (c *Controller) updateWebhookConfigurations(ctx context.Context, caCert []byte) error {
	var validatingWebhook admissionv1.ValidatingWebhookConfiguration
	if err := c.Get(ctx, client.ObjectKey{Name: girtManagerValidatingWebhookConfig}, &validatingWebhook); err != nil {
		return err
	}

	updated := false
	for i := range validatingWebhook.Webhooks {
		if caCertChanged(validatingWebhook.Webhooks[i].ClientConfig.CABundle, caCert) {
			validatingWebhook.Webhooks[i].ClientConfig.CABundle = caCert
			validatingWebhook.Webhooks[i].ClientConfig.Service.Namespace = c.workingNamespace
			validatingWebhook.Webhooks[i].ClientConfig.Service.Name = c.webhookServiceName
			updated = true
		}
	}

	if updated {
		if err := c.Update(ctx, &validatingWebhook); err != nil {
			log.FromContext(ctx).Error(err, "failed to update validating webhook configuration", "name", girtManagerValidatingWebhookConfig)
			return err
		}
		log.FromContext(ctx).Info("updated validating webhook configuration", "name", girtManagerValidatingWebhookConfig)
	}

	var mutatingWebhook admissionv1.MutatingWebhookConfiguration
	if err := c.Get(ctx, client.ObjectKey{Name: girtManagerMutatingWebhookConfig}, &mutatingWebhook); err != nil {
		return err
	}

	updated = false
	for i := range mutatingWebhook.Webhooks {
		if caCertChanged(mutatingWebhook.Webhooks[i].ClientConfig.CABundle, caCert) {
			mutatingWebhook.Webhooks[i].ClientConfig.CABundle = caCert
			mutatingWebhook.Webhooks[i].ClientConfig.Service.Namespace = c.workingNamespace
			mutatingWebhook.Webhooks[i].ClientConfig.Service.Name = c.webhookServiceName
			updated = true
		}
	}

	if updated {
		if err := c.Update(ctx, &mutatingWebhook); err != nil {
			log.FromContext(ctx).Error(err, "failed to update mutating webhook configuration", "name", girtManagerMutatingWebhookConfig)
			return err
		}
		log.FromContext(ctx).Info("updated mutating webhook configuration", "name", girtManagerMutatingWebhookConfig)
	}

	return nil
}

func caCertChanged(old, new []byte) bool {
	return base64.StdEncoding.EncodeToString(old) == base64.StdEncoding.EncodeToString(new)
}

// +kubebuilder:rbac:groups="",resources=secrets,verbs=list;watch;get;update
// +kubebuilder:rbac:groups="admissionregistration.k8s.io",resources=mutatingwebhookconfigurations,verbs=list;watch;get;update
// +kubebuilder:rbac:groups="admissionregistration.k8s.io",resources=validatingwebhookconfigurations,verbs=list;watch;get;update

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("server.secret").
		For(&corev1.Secret{}).
		Watches(&admissionv1.ValidatingWebhookConfiguration{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			return []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{Namespace: c.workingNamespace, Name: c.webhookServerSecretName},
				},
			}
		}), builder.WithPredicates(validatingWebhookConfigPredicate)).
		Watches(&admissionv1.MutatingWebhookConfiguration{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			return []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{Namespace: c.workingNamespace, Name: c.webhookServerSecretName},
				},
			}
		}), builder.WithPredicates(mutatingWebhookConfigPredicate)).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewTypedMaxOfRateLimiter(
				workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](time.Second, 300*time.Second),
				&workqueue.TypedBucketRateLimiter[reconcile.Request]{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
			MaxConcurrentReconciles: 3,
		}).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
