// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package main

import (
	"os"

	"k8s.io/component-base/cli"

	"github.com/kaito-project/grit/cmd/grit-manager/app"
)

func main() {
	command := app.NewGritManagerCommand()
	code := cli.Run(command)
	os.Exit(code)
}
