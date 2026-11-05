package auth

import (
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
)

// Extra keys a TokenReview of a pod-bound service account token reports the pod under.
const (
	ExtraPodName = "authentication.kubernetes.io/pod-name"
	ExtraPodUID  = "authentication.kubernetes.io/pod-uid"
)

const serviceAccountPrefix = "system:serviceaccount:"

// Extra returns the single value of u's extra key, or "" when it has none or several.
func Extra(u authenticationv1.UserInfo, key string) string {
	if v := u.Extra[key]; len(v) == 1 {
		return v[0]
	}
	return ""
}

// ServiceAccountNamespace returns the namespace of a service account username, or "" for any other user.
func ServiceAccountNamespace(username string) string {
	rest, ok := strings.CutPrefix(username, serviceAccountPrefix)
	if !ok {
		return ""
	}
	ns, _, _ := strings.Cut(rest, ":")
	return ns
}
