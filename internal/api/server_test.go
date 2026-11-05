package api

import (
	"strings"
	"testing"

	"github.com/Devcon4/skua/internal/contract/config"
)

func TestNewRequiresCache(t *testing.T) {
	_, err := New(Deps{Config: &config.Config{}})
	if err == nil {
		t.Fatal("New without a cache succeeded")
	}
	if !strings.Contains(err.Error(), "no cache") {
		t.Fatalf("err = %v", err)
	}
}
