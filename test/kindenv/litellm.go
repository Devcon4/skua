package kindenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func (e *env) installLiteLLM(ctx context.Context) error {
	e.logf("installing LiteLLM in namespace gateway")
	ok, err := e.has(ctx, "secret", "litellm-master", "-n", "gateway")
	if err != nil {
		return err
	}
	if !ok {
		if err := e.createObject(ctx, secret("gateway", "litellm-master", map[string][]byte{"masterKey": []byte("sk-" + randomHex(24))})); err != nil {
			return err
		}
	}
	var old corev1.ConfigMap
	if _, err := e.getJSON(ctx, &old, "configmap", "litellm-config", "-n", "gateway"); err != nil {
		return err
	}
	config, err := manifests.ReadFile("manifests/litellm-config.yaml")
	if err != nil {
		return err
	}
	if err := e.applyObject(ctx, &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "litellm-config", Namespace: "gateway"},
		Data:       map[string]string{"config.yaml": string(config)},
	}); err != nil {
		return err
	}
	deployments, err := manifests.ReadFile("manifests/litellm.yaml")
	if err != nil {
		return err
	}
	images, err := deploymentImages(deployments)
	if err != nil {
		return fmt.Errorf("litellm.yaml: %w", err)
	}
	for _, image := range images {
		if err := e.pullOnNodes(ctx, image); err != nil {
			return err
		}
	}
	if err := e.kubectlIn(ctx, deployments, "apply", "-f", "-"); err != nil {
		return err
	}
	if prev := old.Data["config.yaml"]; prev != "" && prev != string(config) {
		if _, err := e.kubectl(ctx, "rollout", "restart", "deployment", "litellm", "-n", "gateway"); err != nil {
			return err
		}
	}
	if _, err := e.kubectl(ctx, "rollout", "status", "deployment", "postgres", "-n", "gateway", "--timeout=180s"); err != nil {
		return err
	}
	if _, err := e.kubectl(ctx, "rollout", "status", "deployment", "litellm", "-n", "gateway", "--timeout=600s"); err != nil {
		return err
	}
	return retryHTTP(ctx, LiteLLMURL+"/health/readiness", 60)
}

func deploymentImages(manifest []byte) ([]string, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	var images []string
	for {
		var doc json.RawMessage
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return images, nil
		}
		if err != nil {
			return nil, err
		}
		if len(doc) == 0 {
			continue
		}
		var meta metav1.TypeMeta
		if err := json.Unmarshal(doc, &meta); err != nil {
			return nil, err
		}
		if meta.Kind != "Deployment" {
			continue
		}
		var d appsv1.Deployment
		if err := json.Unmarshal(doc, &d); err != nil {
			return nil, err
		}
		for _, c := range append(d.Spec.Template.Spec.InitContainers, d.Spec.Template.Spec.Containers...) {
			images = append(images, c.Image)
		}
	}
}

// containerd keeps partly fetched layers, so each retry resumes the pull; images referenced by digest cannot go through `kind load`.
func (e *env) pullOnNodes(ctx context.Context, image string) error {
	nodes, err := e.provider.ListNodes(e.cfg.Name)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if node.CommandContext(ctx, "crictl", "inspecti", image).Run() == nil {
			continue
		}
		for attempt := 1; ; attempt++ {
			if node.CommandContext(ctx, "crictl", "pull", image).Run() == nil {
				break
			}
			if attempt == pullAttempts {
				return fmt.Errorf("pulling %s on %s failed %d times", image, node, pullAttempts)
			}
			e.logf("pull of %s on %s failed (attempt %d), retrying", image, node, attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(min(time.Duration(attempt)*10*time.Second, time.Minute)):
			}
		}
	}
	return nil
}

const pullAttempts = 10

func (e *env) ensureOperatorKey(ctx context.Context) error {
	e.logf("ensuring LiteLLM user and key skua-operator, Secret skua-system/skua-litellm-operator")
	if _, err := e.kubectl(ctx, "delete", "secret", "skua-litellm-operator", "-n", "skua", "--ignore-not-found"); err != nil {
		return err
	}
	ok, err := e.has(ctx, "secret", "skua-litellm-operator", "-n", "skua-system")
	if err != nil {
		return err
	}
	if !ok {
		if err := e.createObject(ctx, secret("skua-system", "skua-litellm-operator", map[string][]byte{"apiKey": []byte("sk-" + randomHex(24))})); err != nil {
			return err
		}
	}
	masterKey, err := e.secretValue(ctx, "gateway", "litellm-master", "masterKey")
	if err != nil {
		return err
	}
	operatorKey, err := e.secretValue(ctx, "skua-system", "skua-litellm-operator", "apiKey")
	if err != nil {
		return err
	}
	master, operator := string(masterKey), string(operatorKey)

	status, err := litellmStatus(ctx, master, http.MethodGet, "/user/info?user_id=skua-operator", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		status, err = litellmStatus(ctx, master, http.MethodPost, "/user/new", map[string]any{"user_id": "skua-operator", "user_role": "internal_user"})
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("LiteLLM /user/new returned %d", status)
		}
	}
	status, err = litellmStatus(ctx, operator, http.MethodGet, "/key/info", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		_, _ = litellmStatus(ctx, master, http.MethodPost, "/key/delete", map[string]any{"key_aliases": []string{"skua-operator"}})
		status, err = litellmStatus(ctx, master, http.MethodPost, "/key/generate", map[string]any{
			"key":            operator,
			"user_id":        "skua-operator",
			"key_alias":      "skua-operator",
			"allowed_routes": []string{"/key/generate", "/key/delete", "/key/info", "/model_group/info"},
		})
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("LiteLLM /key/generate returned %d", status)
		}
	}
	return nil
}

func litellmStatus(ctx context.Context, auth, method, path string, body any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, LiteLLMURL+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+auth)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, fmt.Errorf("LiteLLM %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}
