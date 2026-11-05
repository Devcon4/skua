// Package agentrun holds the AgentRun views and the terminal status write that skua-api's slices share.
package agentrun

import (
	"encoding/json"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/contract/wire"
)

// View is the dev-facing view of run; a run without a phase is Pending.
func View(run *v1alpha1.AgentRun) wire.Run {
	v := wire.Run{
		Name:      run.Name,
		Profile:   run.Spec.Profile,
		ClientKey: run.Spec.ClientKey,
		Phase:     string(run.Status.Phase),
		Reason:    run.Status.Reason,
		CreatedAt: run.CreationTimestamp.UTC(),
	}
	if v.Phase == "" {
		v.Phase = string(v1alpha1.PhasePending)
	}
	if run.Status.Result != nil && len(run.Status.Result.Raw) > 0 {
		v.Result = json.RawMessage(run.Status.Result.Raw)
	}
	if run.Status.Outputs != nil {
		v.Outputs = wire.Outputs{Branch: run.Status.Outputs.Branch, PullRequest: run.Status.Outputs.PullRequest}
	}
	return v
}

// EndData is the data of the end event for run.
func EndData(run *v1alpha1.AgentRun) wire.EndData {
	v := View(run)
	return wire.EndData{Phase: v.Phase, Reason: v.Reason, Result: v.Result, Outputs: v.Outputs}
}
