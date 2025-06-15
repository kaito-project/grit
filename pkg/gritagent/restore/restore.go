// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package restore

import (
	"context"

	"github.com/kaito-project/grit/cmd/grit-agent/app/options"
	"github.com/kaito-project/grit/pkg/gritagent/util"
	"github.com/kaito-project/grit/pkg/metadata"
)

func RunRestore(ctx context.Context, opts *options.GritAgentOptions) error {
	// download checkpointed data from cloud storage
	if err := opts.Syncer.Sync(ctx, opts.SrcDir, opts.DstDir); err != nil {
		return err
	}

	return util.CreateSentinelFile(opts.DstDir, metadata.DownloadSentinelFile)
}
