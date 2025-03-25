// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package checkpoint

import (
	"context"

	"github.com/kaito-project/grit/cmd/grit-agent/app/options"
	"github.com/kaito-project/grit/pkg/gritagent/copy"
)

func RunCheckpoint(ctx context.Context, opts *options.GritAgentOptions) error {
	// execute checkpoint
	if err := RuntimeCheckpointPod(ctx, &opts.RuntimeCheckpointOptions); err != nil {
		return err
	}

	// transfer checkpointed data to cloud storage
	return copy.TransferData(ctx, opts.SrcDir, opts.DstDir)
}
