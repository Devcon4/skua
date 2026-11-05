// Package auth authenticates skua-api callers: devs by OIDC ID token, runners by a pod-bound service account
// token checked with a TokenReview.
package auth

import (
	"net/http"
	"strings"
)

// BearerToken returns the token of an Authorization header with the Bearer scheme in any case; false when
// the header is absent, uses another scheme, or carries an empty token.
func BearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	return token, ok && strings.EqualFold(scheme, "Bearer") && token != ""
}
