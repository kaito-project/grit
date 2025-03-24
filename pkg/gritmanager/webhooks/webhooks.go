// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.

package webhooks

import (
	"github.com/awslabs/operatorpkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/kaito-project/grit/pkg/gritmanager/agentmanager"
	"github.com/kaito-project/grit/pkg/gritmanager/webhooks/checkpoint"
	"github.com/kaito-project/grit/pkg/gritmanager/webhooks/pod"
	"github.com/kaito-project/grit/pkg/gritmanager/webhooks/restore"
)

func NewWebhooks(mgr manager.Manager, agentManager *agentmanager.AgentManager) []controller.Controller {

	return []controller.Controller{
		pod.NewWebook(mgr.GetClient(), agentManager),
		checkpoint.NewCheckpointWebhook(mgr.GetClient()),
		restore.NewRestoreWebhook(mgr.GetClient()),
	}
}
