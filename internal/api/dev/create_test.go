package dev

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestRunName(t *testing.T) {
	sum := sha256.Sum256([]byte("owner\nkey-1"))
	if got, want := RunName("research", "owner", "key-1"), "research-"+hex.EncodeToString(sum[:])[:10]; got != want {
		t.Fatalf("RunName = %q, want %q", got, want)
	}
}
