// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package controllers

import (
	"github.com/awslabs/operatorpkg/controller"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/kaito-project/grit/cmd/grit-manager/app/options"
	"github.com/kaito-project/grit/pkg/gritmanager/agentmanager"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers/checkpoint"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers/restore"
	"github.com/kaito-project/grit/pkg/gritmanager/controllers/secret"
)

// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;patch;update

func NewControllers(mgr manager.Manager, clock clock.Clock, opts *options.GritManagerOptions, agentManager *agentmanager.AgentManager) []controller.Controller {

	return []controller.Controller{
		secret.NewController(clock, mgr.GetClient(), opts.WorkingNamespace, opts.WebhookSecretName, opts.WebhookServiceName, opts.ExpirationDuration),
		checkpoint.NewController(clock, mgr.GetClient(), agentManager),
		restore.NewController(clock, mgr.GetClient(), agentManager),
	}
}
