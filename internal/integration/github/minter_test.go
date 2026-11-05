package github_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/Devcon4/skua/internal/contract/config"
	"github.com/Devcon4/skua/internal/integration/github"
)

const (
	testAppID          = "12345"
	testInstallationID = "678"
)

var (
	testNow  = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	keyOnce  sync.Once
	testKey  *rsa.PrivateKey
	keyError error
)

func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	keyOnce.Do(func() { testKey, keyError = rsa.GenerateKey(rand.Reader, 2048) })
	if keyError != nil {
		t.Fatal(keyError)
	}
	return testKey
}

func pkcs1PEM(t *testing.T) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey(t))})
}

func pkcs8PEM(t *testing.T) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(rsaKey(t))
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func githubConfig(apiURL string) config.GitHub {
	return config.GitHub{
		AppSecret: config.AppSecret{
			Namespace:         "flux-system",
			Name:              "skua-test-github-app",
			AppIDKey:          "githubAppID",
			InstallationIDKey: "githubAppInstallationID",
			PrivateKeyKey:     "githubAppPrivateKey",
		},
		APIURL: apiURL,
		GitURL: "https://github.com",
	}
}

func appSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "flux-system", Name: "skua-test-github-app"},
		Data:       data,
	}
}

func fullSecretData(keyPEM []byte) map[string][]byte {
	return map[string][]byte{
		"githubAppID":             []byte(testAppID),
		"githubAppInstallationID": []byte(testInstallationID + "\n"),
		"githubAppPrivateKey":     keyPEM,
	}
}

type readCounter struct {
	gets, lists atomic.Int32
}

func secretReader(secret *corev1.Secret, counter *readCounter) client.Reader {
	b := fake.NewClientBuilder()
	if secret != nil {
		b = b.WithObjects(secret)
	}
	return b.WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			counter.gets.Add(1)
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			counter.lists.Add(1)
			return c.List(ctx, list, opts...)
		},
	}).Build()
}

type capturedRequest struct {
	method, path, auth string
	body               []byte
}

func recordingServer(t *testing.T, status int, header http.Header, body string) (*httptest.Server, *[]capturedRequest) {
	t.Helper()
	var mu sync.Mutex
	var reqs []capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, capturedRequest{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), body: b})
		mu.Unlock()
		for k, vs := range header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func tokenResponse() string {
	return `{"token":"ghs_minted","expires_at":"` + testNow.Add(time.Hour).Format(time.RFC3339) + `"}`
}

func verifyJWT(t *testing.T, jwt string, pub *rsa.PublicKey) map[string]any {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	enc := base64.RawURLEncoding
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("jwt signature: %v", err)
	}
	var header map[string]any
	headerJSON, _ := enc.DecodeString(parts[0])
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatal(err)
	}
	if header["alg"] != "RS256" {
		t.Fatalf("alg = %v", header["alg"])
	}
	var claims map[string]any
	claimsJSON, _ := enc.DecodeString(parts[1])
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestMintSignsJWTAndNarrowsToOneRepo(t *testing.T) {
	for name, keyPEM := range map[string][]byte{"pkcs1": pkcs1PEM(t), "pkcs8": pkcs8PEM(t)} {
		t.Run(name, func(t *testing.T) {
			srv, reqs := recordingServer(t, http.StatusCreated, nil, tokenResponse())
			var counter readCounter
			m := github.NewMinter(secretReader(appSecret(fullSecretData(keyPEM)), &counter), githubConfig(srv.URL+"/"), srv.Client(), clocktesting.NewFakeClock(testNow))

			tok, err := m.Mint(context.Background(), "acme/widgets")
			if err != nil {
				t.Fatal(err)
			}
			if tok.Value != "ghs_minted" || !tok.ExpiresAt.Equal(testNow.Add(time.Hour)) || !tok.IssuedAt.Equal(testNow) {
				t.Fatalf("token = %+v", tok)
			}
			if len(*reqs) != 1 {
				t.Fatalf("requests = %d", len(*reqs))
			}
			req := (*reqs)[0]
			if req.method != http.MethodPost || req.path != "/app/installations/"+testInstallationID+"/access_tokens" {
				t.Fatalf("request = %s %s", req.method, req.path)
			}
			jwt, ok := strings.CutPrefix(req.auth, "Bearer ")
			if !ok {
				t.Fatalf("authorization = %q", req.auth)
			}
			claims := verifyJWT(t, jwt, &rsaKey(t).PublicKey)
			if claims["iss"] != testAppID {
				t.Fatalf("iss = %v", claims["iss"])
			}
			iat := time.Unix(int64(claims["iat"].(float64)), 0)
			if wallNow := time.Now(); iat.After(wallNow) || iat.Before(wallNow.Add(-time.Minute)) {
				t.Fatalf("iat = %s, want within the last minute of %s", iat, wallNow)
			}
			if exp := time.Unix(int64(claims["exp"].(float64)), 0); exp.Sub(iat) != 2*time.Minute {
				t.Fatalf("exp - iat = %s, want 2m", exp.Sub(iat))
			}
			var body struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			if err := json.Unmarshal(req.body, &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Repositories) != 1 || body.Repositories[0] != "widgets" {
				t.Fatalf("repositories = %v", body.Repositories)
			}
			if len(body.Permissions) != 2 || body.Permissions["contents"] != "write" || body.Permissions["pull_requests"] != "write" {
				t.Fatalf("permissions = %v", body.Permissions)
			}
			if counter.gets.Load() != 1 || counter.lists.Load() != 0 {
				t.Fatalf("gets = %d, lists = %d", counter.gets.Load(), counter.lists.Load())
			}
		})
	}
}

func TestMintReadsSecretOnEveryCall(t *testing.T) {
	srv, _ := recordingServer(t, http.StatusCreated, nil, tokenResponse())
	var counter readCounter
	m := github.NewMinter(secretReader(appSecret(fullSecretData(pkcs1PEM(t))), &counter), githubConfig(srv.URL), srv.Client(), clocktesting.NewFakeClock(testNow))
	for range 3 {
		if _, err := m.Mint(context.Background(), "acme/widgets"); err != nil {
			t.Fatal(err)
		}
	}
	if counter.gets.Load() != 3 || counter.lists.Load() != 0 {
		t.Fatalf("gets = %d, lists = %d", counter.gets.Load(), counter.lists.Load())
	}
}

func TestMintSecretProblemsNameKeyWithoutValue(t *testing.T) {
	keyPEM := pkcs1PEM(t)
	cases := map[string]struct {
		data    func() map[string][]byte
		wantKey string
	}{
		"missing private key": {
			data: func() map[string][]byte {
				d := fullSecretData(keyPEM)
				delete(d, "githubAppPrivateKey")
				return d
			},
			wantKey: "githubAppPrivateKey",
		},
		"missing app id": {
			data: func() map[string][]byte {
				d := fullSecretData(keyPEM)
				delete(d, "githubAppID")
				return d
			},
			wantKey: "githubAppID",
		},
		"missing installation id": {
			data: func() map[string][]byte {
				d := fullSecretData(keyPEM)
				delete(d, "githubAppInstallationID")
				return d
			},
			wantKey: "githubAppInstallationID",
		},
		"unparseable private key": {
			data: func() map[string][]byte {
				d := fullSecretData(keyPEM)
				d["githubAppPrivateKey"] = []byte("-----BEGIN RSA PRIVATE KEY-----\nc2VjcmV0c2VjcmV0\n-----END RSA PRIVATE KEY-----\n")
				return d
			},
			wantKey: "githubAppPrivateKey",
		},
		"non-numeric app id": {
			data: func() map[string][]byte {
				d := fullSecretData(keyPEM)
				d["githubAppID"] = []byte("my-app")
				return d
			},
			wantKey: "githubAppID",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, reqs := recordingServer(t, http.StatusCreated, nil, tokenResponse())
			var counter readCounter
			m := github.NewMinter(secretReader(appSecret(tc.data()), &counter), githubConfig(srv.URL), srv.Client(), clocktesting.NewFakeClock(testNow))
			_, err := m.Mint(context.Background(), "acme/widgets")
			if err == nil {
				t.Fatal("mint succeeded")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantKey) {
				t.Fatalf("error %q does not name %s", msg, tc.wantKey)
			}
			for _, leak := range []string{"BEGIN", "PRIVATE KEY", "c2VjcmV0", testAppID} {
				if strings.Contains(msg, leak) {
					t.Fatalf("error %q leaks %q", msg, leak)
				}
			}
			if len(*reqs) != 0 {
				t.Fatalf("requests = %d", len(*reqs))
			}
		})
	}
}

func TestMintMissingSecret(t *testing.T) {
	var counter readCounter
	m := github.NewMinter(secretReader(nil, &counter), githubConfig("http://127.0.0.1:0"), nil, clocktesting.NewFakeClock(testNow))
	_, err := m.Mint(context.Background(), "acme/widgets")
	if err == nil || !strings.Contains(err.Error(), "flux-system/skua-test-github-app") {
		t.Fatalf("err = %v", err)
	}
}

func TestMintRejectsRepoWithoutOwner(t *testing.T) {
	srv, reqs := recordingServer(t, http.StatusCreated, nil, tokenResponse())
	var counter readCounter
	m := github.NewMinter(secretReader(appSecret(fullSecretData(pkcs1PEM(t))), &counter), githubConfig(srv.URL), srv.Client(), clocktesting.NewFakeClock(testNow))
	for _, repo := range []string{"widgets", "acme/", "/widgets", "acme/widgets/extra"} {
		if _, err := m.Mint(context.Background(), repo); err == nil {
			t.Fatalf("mint %q succeeded", repo)
		}
	}
	if len(*reqs) != 0 || counter.gets.Load() != 0 {
		t.Fatalf("requests = %d, gets = %d", len(*reqs), counter.gets.Load())
	}
}

func TestRemintDue(t *testing.T) {
	tok := github.Token{Value: "ghs_x", IssuedAt: testNow, ExpiresAt: testNow.Add(time.Hour)}
	if github.RemintDue(tok, testNow.Add(29*time.Minute)) {
		t.Fatal("due at 29 minutes")
	}
	if !github.RemintDue(tok, testNow.Add(30*time.Minute)) {
		t.Fatal("not due at 30 minutes")
	}
}

func TestRevoke(t *testing.T) {
	cases := map[string]struct {
		status  int
		wantErr bool
	}{
		"no content":   {status: http.StatusNoContent},
		"unauthorized": {status: http.StatusUnauthorized},
		"server error": {status: http.StatusInternalServerError, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, reqs := recordingServer(t, tc.status, nil, "")
			var counter readCounter
			m := github.NewMinter(secretReader(nil, &counter), githubConfig(srv.URL), srv.Client(), clocktesting.NewFakeClock(testNow))
			err := m.Revoke(context.Background(), "ghs_tok")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v", err)
			}
			req := (*reqs)[0]
			if req.method != http.MethodDelete || req.path != "/installation/token" || req.auth != "Bearer ghs_tok" {
				t.Fatalf("request = %s %s %q", req.method, req.path, req.auth)
			}
			if err != nil && strings.Contains(err.Error(), "ghs_tok") {
				t.Fatalf("error %q leaks the token", err)
			}
			if counter.gets.Load() != 0 {
				t.Fatalf("gets = %d", counter.gets.Load())
			}
		})
	}
}

const (
	refusedBody   = `{"message":"refused"}`
	secondaryBody = `{"message":"You have exceeded a secondary rate limit.","documentation_url":"https://docs.github.com/rest/using-the-rest-api/rate-limits-for-the-rest-api#about-secondary-rate-limits"}`
)

func TestRateLimit(t *testing.T) {
	cases := map[string]struct {
		status   int
		header   http.Header
		body     string
		wantWait time.Duration
		limited  bool
	}{
		"403 with reset": {
			status:   http.StatusForbidden,
			header:   http.Header{"X-Ratelimit-Reset": {strconv.FormatInt(testNow.Add(90*time.Second).Unix(), 10)}, "X-Ratelimit-Remaining": {"0"}},
			body:     refusedBody,
			wantWait: 90 * time.Second,
			limited:  true,
		},
		"429 with no requests remaining": {
			status:   http.StatusTooManyRequests,
			header:   http.Header{"X-Ratelimit-Reset": {strconv.FormatInt(testNow.Add(45*time.Second).Unix(), 10)}, "X-Ratelimit-Remaining": {"0"}},
			body:     refusedBody,
			wantWait: 45 * time.Second,
			limited:  true,
		},
		"403 with no requests remaining and no reset": {
			status:   http.StatusForbidden,
			header:   http.Header{"X-Ratelimit-Remaining": {"0"}},
			body:     refusedBody,
			wantWait: time.Minute,
			limited:  true,
		},
		"secondary 429 with retry-after": {
			status:   http.StatusTooManyRequests,
			header:   http.Header{"Retry-After": {"30"}},
			body:     secondaryBody,
			wantWait: 30 * time.Second,
			limited:  true,
		},
		"secondary 429 with retry-after zero": {
			status:   http.StatusTooManyRequests,
			header:   http.Header{"Retry-After": {"0"}},
			body:     secondaryBody,
			wantWait: time.Second,
			limited:  true,
		},
		"secondary 429 with retry-after date in the past": {
			status:   http.StatusTooManyRequests,
			header:   http.Header{"Retry-After": {testNow.Add(-time.Minute).UTC().Format(http.TimeFormat)}},
			body:     secondaryBody,
			wantWait: time.Second,
			limited:  true,
		},
		"secondary 403 without headers": {
			status:   http.StatusForbidden,
			body:     secondaryBody,
			wantWait: time.Minute,
			limited:  true,
		},
		"403 with reset already passed": {
			status:   http.StatusForbidden,
			header:   http.Header{"X-Ratelimit-Reset": {strconv.FormatInt(testNow.Add(-time.Minute).Unix(), 10)}, "X-Ratelimit-Remaining": {"0"}},
			body:     refusedBody,
			wantWait: time.Second,
			limited:  true,
		},
		"403 with requests remaining": {
			status: http.StatusForbidden,
			header: http.Header{"X-Ratelimit-Reset": {strconv.FormatInt(testNow.Add(time.Hour).Unix(), 10)}, "X-Ratelimit-Remaining": {"4999"}},
			body:   refusedBody,
		},
		"403 without headers": {
			status: http.StatusForbidden,
			body:   refusedBody,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, _ := recordingServer(t, tc.status, tc.header, tc.body)
			var counter readCounter
			m := github.NewMinter(secretReader(appSecret(fullSecretData(pkcs8PEM(t))), &counter), githubConfig(srv.URL), srv.Client(), clocktesting.NewFakeClock(testNow))
			_, err := m.Mint(context.Background(), "acme/widgets")
			if err == nil {
				t.Fatal("mint succeeded")
			}
			var rl *github.RateLimitError
			if errors.As(err, &rl) != tc.limited {
				t.Fatalf("err = %v, want rate limited %v", err, tc.limited)
			}
			if tc.limited && rl.RetryAfter != tc.wantWait {
				t.Fatalf("RetryAfter = %s, want %s", rl.RetryAfter, tc.wantWait)
			}
			if !tc.limited && !strings.Contains(err.Error(), "status 403: refused") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestRevokeRateLimit(t *testing.T) {
	srv, _ := recordingServer(t, http.StatusForbidden, nil, secondaryBody)
	var counter readCounter
	m := github.NewMinter(secretReader(nil, &counter), githubConfig(srv.URL), srv.Client(), clocktesting.NewFakeClock(testNow))
	err := m.Revoke(context.Background(), "ghs_tok")
	var rl *github.RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != time.Minute {
		t.Fatalf("err = %v", err)
	}
}
