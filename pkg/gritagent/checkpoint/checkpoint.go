// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package checkpoint

import (
	"context"

	"github.com/kaito-project/grit/cmd/grit-agent/app/options"
)

func RunCheckpoint(ctx context.Context, opts *options.GritAgentOptions) error {
	return RuntimeCheckpointPod(ctx, &opts.RuntimeCheckpointOptions)
	// TODO: add checkpoint data transfer
}
