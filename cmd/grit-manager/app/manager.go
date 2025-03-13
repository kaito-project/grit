// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package app

import (
	"fmt"
	"net/http"

	"github.com/samber/lo"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/flowcontrol"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kaito-project/grit/cmd/grit-manager/app/options"
	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers"
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

	// controller-runtime manager
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Metrics:                       metricsServerOpts,
		HealthProbeBindAddress:        fmt.Sprintf(":%d", opts.HealthProbePort),
		LeaderElection:                opts.LeaderElection.LeaderElect,
		LeaderElectionID:              opts.LeaderElection.ResourceName,
		LeaderElectionNamespace:       opts.WorkingNamespace,
		LeaderElectionResourceLock:    opts.LeaderElection.ResourceLock,
		LeaderElectionReleaseOnCancel: true,
		Logger:                        logger,
		Cache: cache.Options{
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

	// initialize controllers
	controllers := controllers.NewControllers(mgr, clock.RealClock{}, opts.WorkingNamespace)
	for _, c := range controllers {
		lo.Must0(c.Register(ctx, mgr))
	}

	// start manager
	lo.Must0(mgr.Start(ctx))
	return nil
}
