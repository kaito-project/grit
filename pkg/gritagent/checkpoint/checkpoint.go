// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package checkpoint

import (
	"context"

	"github.com/kaito-project/grit/cmd/grit-agent/app/options"
)

func RunCheckpoint(ctx context.Context, opts *options.GritAgentOptions) error {
	// execute checkpoint
	if err := RuntimeCheckpointPod(ctx, &opts.RuntimeCheckpointOptions); err != nil {
		return err
	}

	// transfer checkpointed data to cloud storage
	return opts.Syncer.Sync(ctx, opts.SrcDir, opts.DstDir)
}
