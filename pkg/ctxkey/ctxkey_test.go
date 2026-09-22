package ctxkey

import (
	"context"
	"testing"
)

func TestRequestID(t *testing.T) {
	ctx := context.Background()
	if got := RequestID(ctx); got != "" {
		t.Fatalf("empty ctx RequestID = %q, want empty", got)
	}
	ctx = WithRequestID(ctx, "abc-123")
	if got := RequestID(ctx); got != "abc-123" {
		t.Fatalf("RequestID = %q, want abc-123", got)
	}
}
