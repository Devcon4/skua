package omprpc

import (
	"context"
	"strings"
	"testing"
)

func TestLargeFramesReassembled(t *testing.T) {
	t.Parallel()
	h := newHarness(t, []string{"e2e-primary"})
	h.mustStart()
	if r := h.prompt("SCRIPT:reply-bytes 1500000"); r.Status != "completed" {
		t.Fatalf("turn = %+v", r)
	}
	text, err := h.a.LastText(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(text) != 1500000 || strings.Trim(text, "x") != "" {
		t.Fatalf("LastText length = %d", len(text))
	}
}
