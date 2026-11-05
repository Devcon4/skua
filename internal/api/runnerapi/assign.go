package runnerapi

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/hub"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
)

// Assign sends the run assignment to the pod's attached stream once the run is Running with its Secret. A stream
// that reattaches for the same run also gets the current forge token. Each stream is assigned at most once.
func (rr *Routes) Assign(ctx context.Context, podName string) {
	st, conn, ok := rr.unassignedStream(podName)
	if !ok {
		return
	}
	log := logger.WithValues("pod", podName)
	run, ok := rr.runningRunOnStream(ctx, log, podName, conn)
	if !ok {
		return
	}
	a, forge, ok, err := rr.assignment(ctx, run)
	if err != nil {
		log.Error(err, "build assignment", "run", run.Name)
		return
	}
	if !ok {
		return
	}
	assignCmd, err := command(wire.CommandAssign, a)
	if err != nil {
		log.Error(err, "encode assign", "run", run.Name)
		return
	}
	rr.deliverAssignment(log, podName, st, conn, run.Name, assignCmd, forge)
}

func (rr *Routes) unassignedStream(podName string) (*podState, *hub.RunnerConn, bool) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	st := rr.pods[podName]
	if st == nil || st.conn == nil || st.assigned == st.conn {
		return nil, nil, false
	}
	return st, st.conn, true
}

func (rr *Routes) runningRunOnStream(ctx context.Context, log logr.Logger, podName string, conn *hub.RunnerConn) (*v1alpha1.AgentRun, bool) {
	var pod corev1.Pod
	key := client.ObjectKey{Namespace: rr.deps.Config.SandboxNamespace, Name: podName}
	if err := rr.deps.APIReader.Get(ctx, key, &pod); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "get pod for assignment")
		}
		return nil, false
	}
	if string(pod.UID) != conn.Pod().UID {
		return nil, false
	}
	run, err := rr.runForPod(ctx, &pod)
	if err != nil {
		log.Error(err, "look up run for assignment")
		return nil, false
	}
	if run == nil || run.Status.Phase != v1alpha1.PhaseRunning {
		return nil, false
	}
	return run, true
}

func (rr *Routes) deliverAssignment(log logr.Logger, podName string, st *podState, conn *hub.RunnerConn, runName string, assignCmd wire.Command, forge *wire.Forge) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	stillUnassigned := st.conn == conn && st.assigned != conn && rr.pods[podName] == st
	if !stillUnassigned {
		return
	}
	cmds := []wire.Command{assignCmd}
	reattachedForSameRun := st.run == runName && forge != nil
	if reattachedForSameRun {
		tok, err := command(wire.CommandToken, wire.Token{Token: forge.Token, ExpiresAt: forge.ExpiresAt})
		if err != nil {
			log.Error(err, "encode token", "run", runName)
			return
		}
		cmds = append(cmds, tok)
	}
	st.assigned, st.run = conn, runName
	if forge != nil {
		st.token = forge.Token
	}
	for _, c := range cmds {
		if err := rr.deps.Hub.Send(podName, runName, c); err != nil {
			log.Info("assignment kept for the next attach", "run", runName, "reason", err.Error())
			return
		}
	}
}

func (rr *Routes) assignment(ctx context.Context, run *v1alpha1.AgentRun) (a wire.Assign, forge *wire.Forge, ok bool, err error) {
	snap := run.Status.ProfileSnapshot
	if snap == nil {
		return a, nil, false, nil
	}
	var secret corev1.Secret
	key := client.ObjectKey{Namespace: rr.deps.Config.RunNamespace, Name: sandbox.SecretName(run.Name)}
	if err := rr.deps.Client.Get(ctx, key, &secret); err != nil {
		return a, nil, false, client.IgnoreNotFound(err)
	}
	gatewayKey := string(secret.Data[sandbox.SecretKeyLiteLLM])
	if gatewayKey == "" {
		return a, nil, false, nil
	}
	forge, err = secretForge(&secret)
	if err != nil {
		return a, nil, false, err
	}
	if forge == nil && snap.Forge == v1alpha1.ForgeGitHub && run.Spec.Workspace != nil {
		return a, nil, false, nil
	}
	a = wire.Assign{
		Run:     run.Name,
		Task:    wire.Task{Prompt: run.Spec.Task.Prompt},
		Gateway: wire.Gateway{BaseURL: rr.deps.Config.Gateway.BaseURL, Key: gatewayKey, Models: slices.Clone(snap.Models)},
		Forge:   forge,
	}
	if s := run.Spec.Task.Schema; s != nil && len(s.Raw) > 0 {
		a.Task.Schema = json.RawMessage(s.Raw)
	}
	if ws := run.Spec.Workspace; ws != nil {
		a.Workspace = &wire.Workspace{Repo: ws.Repo, BaseRef: ws.BaseRef}
	}
	return a, forge, true, nil
}

// PushToken sends secret's forge token to the stream run is assigned to, or assigns the run when it is not yet.
func (rr *Routes) PushToken(ctx context.Context, run *v1alpha1.AgentRun, secret *corev1.Secret) {
	podName := run.Status.PodName
	if podName == "" {
		return
	}
	rr.mu.Lock()
	st := rr.pods[podName]
	assigned := st != nil && st.run == run.Name && st.conn != nil && st.assigned == st.conn
	rr.mu.Unlock()
	if !assigned {
		rr.Assign(ctx, podName)
		return
	}
	log := logger.WithValues("pod", podName, "run", run.Name)
	forge, err := secretForge(secret)
	if err != nil {
		log.Error(err, "read forge token")
		return
	}
	if forge == nil {
		return
	}
	tok, err := command(wire.CommandToken, wire.Token{Token: forge.Token, ExpiresAt: forge.ExpiresAt})
	if err != nil {
		log.Error(err, "encode token")
		return
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if rr.pods[podName] != st || st.conn == nil || st.assigned != st.conn || st.token == forge.Token {
		return
	}
	st.token = forge.Token
	if err := rr.deps.Hub.Send(podName, run.Name, tok); err != nil {
		log.Info("token kept for the next attach", "reason", err.Error())
	}
}

// ForgetRun drops the token of the pods serving run, and their state as soon as their stream is gone.
func (rr *Routes) ForgetRun(run string) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	for name, st := range rr.pods {
		if st.run != run {
			continue
		}
		if st.conn == nil {
			delete(rr.pods, name)
			continue
		}
		st.token = ""
		st.done = true
	}
}

func secretForge(secret *corev1.Secret) (*wire.Forge, error) {
	token := string(secret.Data[sandbox.SecretKeyForgeToken])
	if token == "" {
		return nil, nil
	}
	exp, err := time.Parse(time.RFC3339, string(secret.Data[sandbox.SecretKeyForgeExpiresAt]))
	if err != nil {
		return nil, fmt.Errorf("secret %s/%s: %s: %w", secret.Namespace, secret.Name, sandbox.SecretKeyForgeExpiresAt, err)
	}
	return &wire.Forge{Token: token, ExpiresAt: exp}, nil
}

func command(typ string, data any) (wire.Command, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return wire.Command{}, err
	}
	return wire.Command{Type: typ, Data: raw}, nil
}
