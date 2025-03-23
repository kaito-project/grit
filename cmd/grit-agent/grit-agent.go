// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package main

import (
	"os"

	"k8s.io/component-base/cli"

	"github.com/kaito-project/grit/cmd/grit-agent/app"
)

func main() {
	command := app.NewGritAgentCommand()
	code := cli.Run(command)
	os.Exit(code)
}
