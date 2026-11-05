package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"time"
)

func parsePublicKey(text string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(text))
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("%s does not hold a PEM PUBLIC KEY block", publicKeyEnv)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", publicKeyEnv, err)
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%s is not an RSA key", publicKeyEnv)
	}
	return key, nil
}

func (f *forge) jwtProblem(token string) string {
	const undecodable = "A JSON web token could not be decoded"
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return undecodable
	}
	enc := base64.RawURLEncoding
	rawHeader, err := enc.DecodeString(parts[0])
	if err != nil {
		return undecodable
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(rawHeader, &header); err != nil || header.Alg != "RS256" {
		return "JWT must be signed with RS256"
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		return undecodable
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(f.key, crypto.SHA256, digest[:], sig); err != nil {
		return undecodable
	}
	rawClaims, err := enc.DecodeString(parts[1])
	if err != nil {
		return undecodable
	}
	var claims struct {
		IssuedAt  int64           `json:"iat"`
		ExpiresAt int64           `json:"exp"`
		Issuer    json.RawMessage `json:"iss"`
	}
	if err := json.Unmarshal(rawClaims, &claims); err != nil {
		return undecodable
	}
	if iss := string(claims.Issuer); iss == "" || iss == "null" || iss == `""` {
		return "'Issuer' claim ('iss') must be the App ID"
	}
	now := f.now()
	if time.Unix(claims.IssuedAt, 0).After(now.Add(jwtClockSkew)) {
		return "'Issued at' claim ('iat') must be an Integer representing a time in the past"
	}
	exp := time.Unix(claims.ExpiresAt, 0)
	if !exp.After(now) {
		return "'Expiration time' claim ('exp') must be a numeric value representing the future time at which the assertion expires"
	}
	if exp.After(now.Add(jwtMaxFuture)) {
		return "'Expiration time' claim ('exp') is too far in the future"
	}
	return ""
}
