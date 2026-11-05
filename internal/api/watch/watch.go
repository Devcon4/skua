// Package watch feeds AgentRun and per-run Secret changes from skua-api's cache to the event hub and the runner
// routes.
package watch

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/contract/sandbox"
)

// +kubebuilder:rbac:groups="",namespace=skua,resources=secrets,verbs=get;list;watch

const timeout = 10 * time.Second

var logger = ctrllog.Log.WithName("api").WithName("runner")

// Runner is what the runner routes do when a run or its Secret changes.
type Runner interface {
	Assign(ctx context.Context, podName string)
	PushToken(ctx context.Context, run *v1alpha1.AgentRun, secret *corev1.Secret)
	ForgetRun(run string)
}

// Deps are the dependencies of the watch. Client reads AgentRuns from the cache.
type Deps struct {
	Config *config.Config
	Client client.Reader
	Hub    *hub.Hub
	Runner Runner
}

type watcher struct {
	deps Deps
}

// Register adds AgentRun and Secret event handlers to informers, which must not be started yet. A phase change
// appends a status event to the run's log, and a terminal phase also the end event; a Running run or a changed
// gateway key or forge token goes to Runner.
func Register(informers cache.Informers, d Deps) error {
	if d.Config == nil || d.Client == nil || d.Hub == nil || d.Runner == nil {
		return errors.New("api: watch: Config, Client, Hub and Runner are required")
	}
	w := &watcher{deps: d}
	ctx := context.Background()
	runs, err := informers.GetInformer(ctx, &v1alpha1.AgentRun{}, cache.BlockUntilSynced(false))
	if err != nil {
		return fmt.Errorf("api: AgentRun informer: %w", err)
	}
	if _, err := runs.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if run, ok := w.watchedRun(obj); ok && run.Status.Phase == v1alpha1.PhaseRunning {
				w.withTimeout(func(ctx context.Context) { d.Runner.Assign(ctx, run.Status.PodName) })
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			prev, ok := w.watchedRun(oldObj)
			run, ok2 := w.watchedRun(newObj)
			if ok && ok2 {
				w.runChanged(prev, run)
			}
		},
		DeleteFunc: func(obj any) {
			if tomb, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
				obj = tomb.Obj
			}
			if run, ok := w.watchedRun(obj); ok {
				d.Hub.Forget(run.Name)
				d.Runner.ForgetRun(run.Name)
			}
		},
	}); err != nil {
		return fmt.Errorf("api: AgentRun handler: %w", err)
	}

	secrets, err := informers.GetInformer(ctx, &corev1.Secret{}, cache.BlockUntilSynced(false))
	if err != nil {
		return fmt.Errorf("api: Secret informer: %w", err)
	}
	if _, err := secrets.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) { w.secretChanged(nil, obj) },
		UpdateFunc: func(oldObj, newObj any) {
			w.secretChanged(oldObj, newObj)
		},
	}); err != nil {
		return fmt.Errorf("api: Secret handler: %w", err)
	}
	return nil
}

func (w *watcher) watchedRun(obj any) (*v1alpha1.AgentRun, bool) {
	run, ok := obj.(*v1alpha1.AgentRun)
	return run, ok && run.Namespace == w.deps.Config.RunNamespace
}

func (w *watcher) secretChanged(oldObj, newObj any) {
	secret, ok := newObj.(*corev1.Secret)
	if !ok || secret.Namespace != w.deps.Config.RunNamespace || secret.Labels[sandbox.LabelRunSecret] != "true" {
		return
	}
	if old, ok := oldObj.(*corev1.Secret); ok &&
		string(old.Data[sandbox.SecretKeyForgeToken]) == string(secret.Data[sandbox.SecretKeyForgeToken]) &&
		string(old.Data[sandbox.SecretKeyLiteLLM]) == string(secret.Data[sandbox.SecretKeyLiteLLM]) {
		return
	}
	runName := secret.Labels[sandbox.LabelRunName]
	if runName == "" || secret.Name != sandbox.SecretName(runName) {
		return
	}
	w.withTimeout(func(ctx context.Context) {
		var run v1alpha1.AgentRun
		if err := w.deps.Client.Get(ctx, client.ObjectKey{Namespace: secret.Namespace, Name: runName}, &run); err != nil {
			if client.IgnoreNotFound(err) != nil {
				logger.Error(err, "get run of secret", "secret", secret.Name)
			}
			return
		}
		if run.Status.Phase == v1alpha1.PhaseRunning {
			w.deps.Runner.PushToken(ctx, &run, secret)
		}
	})
}

func (w *watcher) withTimeout(f func(context.Context)) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	f(ctx)
}
