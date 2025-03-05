// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package injections

import (
	"fmt"
	"runtime"
)

var (
	Version   = "v0.0.1"
	GitCommit = "unknown"
	BuildDate = "2025-03-07T00:00:00Z"
	GoVersion = runtime.Version()
)

func VersionInfo() string {
	return fmt.Sprintf("%s (Git Commit: %s, Build Date: %s, Go Version: %s)", Version, GitCommit, BuildDate, GoVersion)
}
