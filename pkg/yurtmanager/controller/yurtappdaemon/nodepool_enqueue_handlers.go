/*
Copyright 2021 The OpenYurt Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package yurtappdaemon

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openyurtio/openyurt/pkg/apis/apps/v1alpha1"
)

type EnqueueYurtAppDaemonForNodePool struct {
	client client.Client
}

func (e *EnqueueYurtAppDaemonForNodePool) Create(ctx context.Context, event event.CreateEvent, limitingInterface workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	e.addAllYurtAppDaemonToWorkQueue(limitingInterface)
}

func (e *EnqueueYurtAppDaemonForNodePool) Update(ctx context.Context, event event.UpdateEvent, limitingInterface workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	e.addAllYurtAppDaemonToWorkQueue(limitingInterface)
}

func (e *EnqueueYurtAppDaemonForNodePool) Delete(ctx context.Context, event event.DeleteEvent, limitingInterface workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	e.addAllYurtAppDaemonToWorkQueue(limitingInterface)
}

func (e *EnqueueYurtAppDaemonForNodePool) Generic(ctx context.Context, event event.GenericEvent, limitingInterface workqueue.TypedRateLimitingInterface[reconcile.Request]) {
}

func (e *EnqueueYurtAppDaemonForNodePool) addAllYurtAppDaemonToWorkQueue(limitingInterface workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	ydas := &v1alpha1.YurtAppDaemonList{}
	if err := e.client.List(context.TODO(), ydas); err != nil {
		return
	}

	for _, ud := range ydas.Items {
		addYurtAppDaemonToWorkQueue(ud.GetNamespace(), ud.GetName(), limitingInterface)
	}
}

var _ handler.EventHandler = &EnqueueYurtAppDaemonForNodePool{}

// addYurtAppDaemonToWorkQueue adds the YurtAppDaemon the reconciler's workqueue
func addYurtAppDaemonToWorkQueue(namespace, name string,
	q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	q.Add(reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
	})
}
