// Package condition sets and reads status conditions for the operator's reconcilers.
package condition

import (
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Set writes the condition condType into conds, stamped with generation. LastTransitionTime is since
// unless the existing condition has the same status and reason, in which case it is kept. Unlike
// meta.SetStatusCondition, a reason change also moves LastTransitionTime, so it dates the current reason.
func Set(conds *[]metav1.Condition, generation int64, condType string, status metav1.ConditionStatus, reason, message string, since time.Time) {
	c := metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
		LastTransitionTime: metav1.NewTime(since),
	}
	for i := range *conds {
		old := &(*conds)[i]
		if old.Type != condType {
			continue
		}
		if old.Status == status && old.Reason == reason {
			c.LastTransitionTime = old.LastTransitionTime
		}
		*old = c
		return
	}
	*conds = append(*conds, c)
}

// FailingSince returns the LastTransitionTime of condType when that condition is False, and false otherwise.
func FailingSince(conds []metav1.Condition, condType string) (time.Time, bool) {
	c := meta.FindStatusCondition(conds, condType)
	if c == nil || c.Status != metav1.ConditionFalse {
		return time.Time{}, false
	}
	return c.LastTransitionTime.Time, true
}
