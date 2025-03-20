// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/flowcontrol"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	runtimecache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	runtimewebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	"github.com/kaito-project/grit/cmd/grit-manager/app/options"
	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
	"github.com/kaito-project/grit/pkg/gritmanager/agentmanager"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers/util"
	"github.com/kaito-project/grit/pkg/gritmanager/webhooks"
	"github.com/kaito-project/grit/pkg/injections"
	"github.com/kaito-project/grit/pkg/util/profile"
)

const (
	GritManager = "grit-manager"
)

func init() {
	// controller-runtime manager use scheme.Scheme by default
	v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
}

func NewGritManagerCommand() *cobra.Command {
	opts := options.NewGritManagerOptions()

	cmd := &cobra.Command{
		Use:     GritManager,
		Version: injections.VersionInfo(),
		Run: func(cmd *cobra.Command, args []string) {
			cliflag.PrintFlags(cmd.Flags())

			if err := Run(opts); err != nil {
				klog.Fatalf("run grit-manager failed, %v", err)
			}
		},
	}

	globalflag.AddGlobalFlags(cmd.Flags(), cmd.Name())
	opts.AddFlags(cmd.Flags())

	return cmd
}

func Run(opts *options.GritManagerOptions) error {
	ctx := ctrl.SetupSignalHandler()

	//logging
	logger := klog.FromContext(ctx)
	log.SetLogger(logger)
	klog.SetLogger(logger)

	// metrics server options
	metricsServerOpts := metricsserver.Options{
		BindAddress:   fmt.Sprintf(":%d", opts.MetricsPort),
		ExtraHandlers: make(map[string]http.Handler),
	}

	if opts.EnableProfiling {
		for path, handler := range profile.PprofHandlers {
			metricsServerOpts.ExtraHandlers[path] = handler
		}
	}

	// prepare rest config
	cfg := ctrl.GetConfigOrDie()
	cfg.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(float32(opts.KubeClientQPS), opts.KubeClientBurst)
	cfg.UserAgent = GritManager

	// trim managed fields
	trimManagedFields := func(obj interface{}) (interface{}, error) {
		if accessor, err := meta.Accessor(obj); err == nil {
			if accessor.GetManagedFields() != nil {
				accessor.SetManagedFields(nil)
			}
		}
		return obj, nil
	}

	// prepare webhook server secret lister and grit agent config lister
	secretLister, configmapLister, err := prepareResourcesLister(ctx, cfg, opts.WorkingNamespace)
	if err != nil {
		return err
	}

	// controller-runtime manager
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Metrics:                       metricsServerOpts,
		HealthProbeBindAddress:        fmt.Sprintf(":%d", opts.HealthProbePort),
		LeaderElection:                opts.LeaderElection.LeaderElect,
		LeaderElectionID:              opts.LeaderElection.ResourceName,
		LeaderElectionNamespace:       opts.WorkingNamespace,
		LeaderElectionResourceLock:    opts.LeaderElection.ResourceLock,
		LeaderElectionReleaseOnCancel: true,
		WebhookServer: runtimewebhook.NewServer(runtimewebhook.Options{
			Port: opts.WebhookPort,
			TLSOpts: []func(*tls.Config){
				func(cfg *tls.Config) {
					cfg.MinVersion = tls.VersionTLS13
					// If we return (nil, error), the client sees - 'tls: internal error'
					// If we return (nil, nil) the client sees - 'tls: no certificates configured'
					cfg.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
						// return (nil, nil) when we don't find a certificate
						secret, err := secretLister.Secrets(opts.WorkingNamespace).Get(opts.WebhookSecretName)
						if err != nil {
							return nil, nil //nolint:nilerr
						}

						serverKey, ok := secret.Data[util.ServerKey]
						if !ok {
							return nil, nil
						}
						serverCert, ok := secret.Data[util.ServerCert]
						if !ok {
							return nil, nil
						}

						cert, err := tls.X509KeyPair(serverCert, serverKey)
						if err != nil {
							return nil, err
						}
						return &cert, nil
					}
				},
			},
		}),
		Logger: logger,
		Cache: runtimecache.Options{
			DefaultTransform: trimManagedFields,
		},
	})
	if err != nil {
		klog.Errorf("failed to new manager, %v", err)
		return err
	}

	// endpoints for liveness and readiness
	lo.Must0(mgr.AddHealthzCheck("healthz", healthz.Ping))
	lo.Must0(mgr.AddReadyzCheck("readyz", healthz.Ping))

	// initialize girt agent manager
	agentManager := agentmanager.NewAgentManager(opts.WorkingNamespace, configmapLister)

	// initialize controllers
	controllers := controllers.NewControllers(mgr, clock.RealClock{}, opts, agentManager)
	for _, c := range controllers {
		lo.Must0(c.Register(ctx, mgr))
	}

	// initialize webhooks
	webhooks := webhooks.NewWebhooks(mgr, agentManager)
	for _, c := range webhooks {
		lo.Must0(c.Register(ctx, mgr))
	}

	// start manager
	lo.Must0(mgr.Start(ctx))
	return nil
}

func prepareResourcesLister(ctx context.Context, cfg *rest.Config, ns string) (corev1listers.SecretLister, corev1listers.ConfigMapLister, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, err
	}

	factory := informers.NewSharedInformerFactoryWithOptions(clientset, 24*time.Hour, informers.WithNamespace(ns))
	secretInformer := factory.Core().V1().Secrets().Informer()
	secretLister := factory.Core().V1().Secrets().Lister()

	configmapInformer := factory.Core().V1().ConfigMaps().Informer()
	configmapLister := factory.Core().V1().ConfigMaps().Lister()

	go factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), secretInformer.HasSynced, configmapInformer.HasSynced) {
		return nil, nil, errors.New("failed to wait for syncing cache for webhook server secret or grit agent config")
	}

	return secretLister, configmapLister, nil
}
