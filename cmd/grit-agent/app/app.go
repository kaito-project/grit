// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package app

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes/scheme"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kaito-project/grit/cmd/grit-agent/app/options"
	"github.com/kaito-project/grit/pkg/apis/v1alpha1"
	"github.com/kaito-project/grit/pkg/gritagent/checkpoint"
	"github.com/kaito-project/grit/pkg/gritagent/restore"
	"github.com/kaito-project/grit/pkg/gritagent/syncer/file"
	"github.com/kaito-project/grit/pkg/injections"
)

func init() {
	v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
}

func NewGritAgentCommand() *cobra.Command {
	opts := options.NewGritAgentOptions()

	cmd := &cobra.Command{
		Use:     "grit-agent",
		Version: injections.VersionInfo(),
		RunE: func(cmd *cobra.Command, args []string) error {
			cliflag.PrintFlags(cmd.Flags())

			if err := Run(opts); err != nil {
				fmt.Fprintf(os.Stderr, "run grit-agent failed: %v\n", err)
				return err
			}
			return nil
		},
	}

	globalflag.AddGlobalFlags(cmd.Flags(), cmd.Name())
	opts.AddFlags(cmd.Flags())

	return cmd
}

func Run(opts *options.GritAgentOptions) error {
	ctx := ctrl.SetupSignalHandler()

	//logging
	logger := klog.FromContext(ctx)
	log.SetLogger(logger)

	// init checkpointed data syncer
	opts.Syncer = file.NewFileSyncer()

	var handler func(context.Context, *options.GritAgentOptions) error

	switch opts.Action {
	case options.ActionCheckpoint:
		handler = checkpoint.RunCheckpoint
	case options.ActionRestore:
		handler = restore.RunRestore
	default:
		return fmt.Errorf("unknown action %s", opts.Action)
	}

	return handler(ctx, opts)
}
