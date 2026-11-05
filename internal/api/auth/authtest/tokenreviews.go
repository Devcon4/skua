package authtest

import (
	"slices"
	"sync"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	authenticationv1client "k8s.io/client-go/kubernetes/typed/authentication/v1"
	clienttesting "k8s.io/client-go/testing"

	"github.com/Devcon4/skua/internal/api/auth"
)

// TokenReviews is a fake TokenReview API that authenticates the tokens added to it.
type TokenReviews struct {
	mu     sync.Mutex
	tokens map[string]Token
	cs     *k8sfake.Clientset
}

// Token is what a review of one token reports: a service account token bound to a pod, valid for Audience.
type Token struct {
	Audience       string
	Namespace      string
	ServiceAccount string
	PodName        string
	PodUID         string
}

func NewTokenReviews() *TokenReviews {
	r := &TokenReviews{tokens: map[string]Token{}, cs: k8sfake.NewClientset()}
	r.cs.PrependReactor("create", "tokenreviews", r.react)
	return r
}

// Client is the TokenReview client to hand to skua-api.
func (r *TokenReviews) Client() authenticationv1client.TokenReviewInterface {
	return r.cs.AuthenticationV1().TokenReviews()
}

// Add makes token authenticate as tok and returns token. A review of any other token, or one that does not
// ask for tok.Audience, is unauthenticated.
func (r *TokenReviews) Add(token string, tok Token) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens[token] = tok
	return token
}

func (r *TokenReviews) react(a clienttesting.Action) (bool, runtime.Object, error) {
	review := a.(clienttesting.CreateAction).GetObject().(*authenticationv1.TokenReview).DeepCopy()
	r.mu.Lock()
	tok, ok := r.tokens[review.Spec.Token]
	r.mu.Unlock()
	if !ok || !slices.Contains(review.Spec.Audiences, tok.Audience) {
		review.Status = authenticationv1.TokenReviewStatus{Error: "token is invalid for the requested audiences"}
		return true, review, nil
	}
	review.Status = authenticationv1.TokenReviewStatus{
		Authenticated: true,
		Audiences:     []string{tok.Audience},
		User: authenticationv1.UserInfo{
			Username: "system:serviceaccount:" + tok.Namespace + ":" + tok.ServiceAccount,
			Extra: map[string]authenticationv1.ExtraValue{
				auth.ExtraPodName: {tok.PodName},
				auth.ExtraPodUID:  {tok.PodUID},
			},
		},
	}
	return true, review, nil
}
