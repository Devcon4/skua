package runnerapi

import (
	"fmt"
	"net/http"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/Devcon4/skua/api/v1alpha1"
	"github.com/Devcon4/skua/internal/api/agentrun"
	"github.com/Devcon4/skua/internal/contract/sandbox"
	"github.com/Devcon4/skua/internal/contract/wire"
)

// MaxResultBodyBytes is the largest result body the result route reads.
const MaxResultBodyBytes = 2 << 20

func (rr *Routes) result(w http.ResponseWriter, r *http.Request) {
	pod, ok := rr.authenticate(w, r)
	if !ok {
		return
	}
	var res wire.RunnerResult
	if err := decode(w, r, MaxResultBodyBytes, &res); err != nil {
		if ref := decodeRefusal(err); ref.code != http.StatusRequestEntityTooLarge {
			refuse(w, ref)
			return
		}
		run, err := rr.runForPod(r.Context(), pod)
		if err != nil {
			refuse(w, &refusal{http.StatusInternalServerError, wire.RefusalInternalError, err.Error()})
			return
		}
		rr.refuseResult(w, r, run, fmt.Sprintf("result body exceeds %d bytes", MaxResultBodyBytes))
		return
	}
	run, ok := rr.callerRun(r.Context(), w, pod, res.Run)
	if !ok {
		return
	}
	phase := v1alpha1.RunPhase(res.Phase)
	if !phase.Terminal() {
		refuse(w, &refusal{http.StatusBadRequest, wire.RefusalBadRequest, "phase must be Succeeded or Failed"})
		return
	}
	if run.Status.Phase.Terminal() {
		w.WriteHeader(http.StatusOK)
		return
	}
	limit := int64(sandbox.DefaultMaxResultBytes)
	if snap := run.Status.ProfileSnapshot; snap != nil && snap.Limits.MaxResultBytes > 0 {
		limit = snap.Limits.MaxResultBytes
	}
	if size := int64(len(res.Result)); size > limit {
		rr.refuseResult(w, r, run, fmt.Sprintf("result is %d bytes, the limit is %d", size, limit))
		return
	}
	err := agentrun.Finish(r.Context(), rr.deps.Client, rr.deps.APIReader, rr.deps.Clock, run, func(st *v1alpha1.AgentRunStatus) {
		st.Phase, st.Reason = phase, res.Reason
		st.Result = nil
		if len(res.Result) > 0 {
			st.Result = &runtime.RawExtension{Raw: res.Result}
		}
		st.Outputs = nil
		if res.Outputs != (wire.Outputs{}) {
			st.Outputs = &v1alpha1.RunOutputs{Branch: res.Outputs.Branch, PullRequest: res.Outputs.PullRequest}
		}
	})
	if err != nil {
		refuse(w, &refusal{http.StatusInternalServerError, wire.RefusalInternalError, err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (rr *Routes) refuseResult(w http.ResponseWriter, r *http.Request, run *v1alpha1.AgentRun, msg string) {
	if run != nil {
		err := agentrun.Finish(r.Context(), rr.deps.Client, rr.deps.APIReader, rr.deps.Clock, run, func(st *v1alpha1.AgentRunStatus) {
			st.Phase, st.Reason, st.Message, st.Result = v1alpha1.PhaseFailed, v1alpha1.ReasonResultTooLarge, msg, nil
		})
		if err != nil {
			refuse(w, &refusal{http.StatusInternalServerError, wire.RefusalInternalError, err.Error()})
			return
		}
	}
	refuse(w, &refusal{http.StatusRequestEntityTooLarge, v1alpha1.ReasonResultTooLarge, msg})
}
