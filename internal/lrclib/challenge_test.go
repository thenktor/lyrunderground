package lrclib

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
)

func TestSolveChallenge(t *testing.T) {
	// About one in 256 nonces fulfills this target, so the test stays fast.
	target := "0F" + strings.Repeat("FF", 31)
	nonce, err := SolveChallenge(context.Background(), "prefix", target)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("prefix" + nonce))
	targetBytes, err := hex.DecodeString(target)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Compare(sum[:], targetBytes) > 0 {
		t.Fatalf("nonce %s does not satisfy target", nonce)
	}
	if _, err := strconv.ParseUint(nonce, 10, 64); err != nil {
		t.Fatalf("nonce %q is not decimal: %v", nonce, err)
	}
}

func TestSolveChallengeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := SolveChallenge(ctx, "prefix", strings.Repeat("00", 32))
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestSolveChallengeInvalidTarget(t *testing.T) {
	if _, err := SolveChallenge(context.Background(), "prefix", "xyz"); err == nil {
		t.Fatal("expected invalid target error")
	}
}
