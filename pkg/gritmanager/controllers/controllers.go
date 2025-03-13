// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package controllers

import (
	"github.com/awslabs/operatorpkg/controller"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/kaito-project/grit/pkg/gritmanager/controllers/checkpoint"
)

func NewControllers(mgr manager.Manager, clock clock.Clock, workingNamespace string) []controller.Controller {

	return []controller.Controller{
		checkpoint.NewController(clock, mgr.GetClient(), workingNamespace),
	}
}
