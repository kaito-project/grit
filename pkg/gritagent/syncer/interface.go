// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package syncer

import (
	"context"
)

// Syncer defines the ability to transfer files between cloud storage and local disk.
type Syncer interface {
	Name() string
	// Sync copy all files from srcDir to dstDir, while keeping the file directory structure unchanged.
	Sync(ctx context.Context, srcDir, dstDir string) error
}
