package kindenv

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

func (e *env) ensureGitHubApp(ctx context.Context) error {
	e.logf("creating fake GitHub App Secret flux-system/skua-test-github-app")
	ok, err := e.has(ctx, "secret", "skua-test-github-app", "-n", "flux-system")
	if err != nil {
		return err
	}
	if !ok {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return err
		}
		app := secret("flux-system", "skua-test-github-app", map[string][]byte{
			"githubAppID":             []byte("123456"),
			"githubAppInstallationID": []byte("12345678"),
			"githubAppPrivateKey":     pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
		})
		app.Labels = map[string]string{"skua.dev/fake": "true"}
		if err := e.createObject(ctx, app); err != nil {
			return err
		}
	}
	if err := e.applyObject(ctx, secret("flux-system", "other-secret", map[string][]byte{"x": []byte("y")})); err != nil {
		return err
	}
	keyPEM, err := e.secretValue(ctx, "flux-system", "skua-test-github-app", "githubAppPrivateKey")
	if err != nil {
		return err
	}
	pub, err := publicKeyPEM(keyPEM)
	if err != nil {
		return fmt.Errorf("flux-system/skua-test-github-app: %w", err)
	}
	return e.applyObject(ctx, secret("skua-e2e", "forge-app", map[string][]byte{"appPublicKey": pub}))
}

func publicKeyPEM(privatePEM []byte) ([]byte, error) {
	block, _ := pem.Decode(privatePEM)
	if block == nil {
		return nil, errors.New("private key is not PEM")
	}
	var key any
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		if key, err = x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
	}
	signer, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	der, err := x509.MarshalPKIXPublicKey(&signer.PublicKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}
