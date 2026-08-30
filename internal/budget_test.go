package internal

import (
	"math"
	"net/http"
	"strings"
	"testing"
)

// The estimate drives a refusal, so it must not undercount what the encoder
// actually writes.
func TestPropEchoSizeCoversWhatTheEncoderWrites(t *testing.T) {
	space := "urn:" + strings.Repeat("a", 500)
	prop := propWithNames(4, space, "p")

	estimate := PropEchoSize(prop)
	if estimate < 4*(len(space)+1) {
		t.Errorf("PropEchoSize = %d, below the raw name bytes it must at least cover", estimate)
	}
}

func TestPropEchoSizeIgnoresNilAndAllProp(t *testing.T) {
	if n := PropEchoSize(nil); n != 0 {
		t.Errorf("PropEchoSize(nil) = %d, want 0; allprop cannot be grown by the client", n)
	}
}

func TestBoundResponseWorkRefusesTheProduct(t *testing.T) {
	// Either factor alone is within its own bound.
	if err := BoundResponseWork(100, 1, 1000); err != nil {
		t.Errorf("one resource should be accepted: %v", err)
	}
	if err := BoundResponseWork(1, 100, 1000); err != nil {
		t.Errorf("a small echo should be accepted: %v", err)
	}

	err := BoundResponseWork(100, 100, 1000)
	if err == nil {
		t.Fatal("expected the product to be refused")
	}
	httpErr := HTTPErrorFromError(err)
	if httpErr.Code != http.StatusInsufficientStorage {
		t.Errorf("code = %d, want 507", httpErr.Code)
	}
}

func TestBoundResponseWorkLimitConvention(t *testing.T) {
	if err := BoundResponseWork(MaxResponsePropBytes, 2, 0); err == nil {
		t.Error("a zero limit should apply MaxResponsePropBytes")
	}
	if err := BoundResponseWork(MaxResponsePropBytes, 1_000_000, -1); err != nil {
		t.Errorf("a negative limit should remove the bound: %v", err)
	}
}

// echo x resources is what an attacker maximises, so the check must not wrap.
func TestBoundResponseWorkDoesNotOverflow(t *testing.T) {
	if err := BoundResponseWork(math.MaxInt/2, 4, 0); err == nil {
		t.Error("a product past MaxInt should be refused, not wrapped into acceptance")
	}
}

func TestBoundResponseWorkAcceptsAnEmptyResult(t *testing.T) {
	if err := BoundResponseWork(math.MaxInt, 0, 0); err != nil {
		t.Errorf("no resources means no echo: %v", err)
	}
}
