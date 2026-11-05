//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	sandboxextv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	skuav1alpha1 "github.com/Devcon4/skua/api/v1alpha1"
)

var (
	kubeContext string
	k8s         client.Client
	clientset   kubernetes.Interface
)

func TestMain(m *testing.M) {
	os.Exit(runSuite(m))
}

func runSuite(m *testing.M) int {
	if err := connect(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	defer stopPortForwards()
	if err := waitProfilesReady("fake", "generic"); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		return 1
	}
	return m.Run()
}

func waitProfilesReady(names ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var last string
	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		for _, name := range names {
			var p skuav1alpha1.AgentProfile
			if err := k8s.Get(ctx, client.ObjectKey{Name: name}, &p); err != nil {
				last = err.Error()
				return false, nil
			}
			ready := meta.FindStatusCondition(p.Status.Conditions, skuav1alpha1.ConditionReady)
			if ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != p.Generation {
				last = fmt.Sprintf("profile %s not Ready: %+v", name, ready)
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("fixture profiles %v not Ready within 3m: %s", names, last)
	}
	return nil
}

func connect() error {
	raw, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	if !strings.HasPrefix(raw.CurrentContext, "kind-") {
		return fmt.Errorf("refusing to run against context %q: only kind- contexts are allowed", raw.CurrentContext)
	}
	cfg, err := clientcmd.NewDefaultClientConfig(*raw, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return fmt.Errorf("build rest config: %w", err)
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		skuav1alpha1.AddToScheme,
		sandboxv1beta1.AddToScheme,
		sandboxextv1beta1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			return fmt.Errorf("build scheme: %w", err)
		}
	}
	if k8s, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		return fmt.Errorf("controller-runtime client: %w", err)
	}
	if clientset, err = kubernetes.NewForConfig(cfg); err != nil {
		return fmt.Errorf("clientset: %w", err)
	}
	kubeContext = raw.CurrentContext
	return nil
}

func TestLogin(t *testing.T) {
	t.Parallel()
	verifier := dexProvider(t).Verifier(&oidc.Config{ClientID: ompClientID})
	for _, tc := range []struct {
		email string
		admin bool
	}{
		{email: "admin@skua.test", admin: true},
		{email: "dev@skua.test", admin: false},
	} {
		t.Run(tc.email, func(t *testing.T) {
			t.Parallel()
			raw := login(t, tc.email)
			ctx, cancel := context.WithTimeout(oidc.ClientContext(context.Background(), dexHTTP), requestTimeout)
			defer cancel()
			tok, err := verifier.Verify(ctx, raw)
			if err != nil {
				t.Fatalf("verify id_token: %v", err)
			}
			if tok.Issuer != dexIssuer {
				t.Errorf("iss = %q, want %q", tok.Issuer, dexIssuer)
			}
			if !slices.Equal(tok.Audience, []string{ompClientID}) {
				t.Errorf("aud = %q, want [%q]", tok.Audience, ompClientID)
			}
			var claims struct {
				Email  string   `json:"email"`
				Groups []string `json:"groups"`
			}
			if err := tok.Claims(&claims); err != nil {
				t.Fatalf("claims: %v", err)
			}
			if claims.Email != tc.email {
				t.Errorf("email = %q, want %q", claims.Email, tc.email)
			}
			if got := slices.Contains(claims.Groups, "skua-admins"); got != tc.admin {
				t.Errorf("groups = %q: skua-admins present %t, want %t", claims.Groups, got, tc.admin)
			}
			if _, code := (apiClient{token: raw}).profiles(t); code != http.StatusOK {
				t.Errorf("GET /profiles with token: status %d, want 200", code)
			}
		})
	}
	if _, code := (apiClient{}).profiles(t); code != http.StatusUnauthorized {
		t.Errorf("GET /profiles without token: status %d, want 401", code)
	}
}
