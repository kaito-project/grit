// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package controllers

import (
	"github.com/awslabs/operatorpkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func NewControllers(mgr manager.Manager) []controller.Controller {

	return []controller.Controller{}
}
