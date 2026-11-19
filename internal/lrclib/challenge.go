package lrclib

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

// SolveChallenge finds a decimal nonce such that
//
//	SHA256(prefix + nonce) <= target
//
// with both hashes compared as big-endian byte strings. This is the proof of
// work required for POST /api/publish.
func SolveChallenge(ctx context.Context, prefix, targetHex string) (string, error) {
	if prefix == "" {
		return "", errors.New("leerer Challenge-Prefix")
	}
	target, err := hex.DecodeString(targetHex)
	if err != nil {
		return "", fmt.Errorf("ungültiges Challenge-Ziel: %w", err)
	}
	if len(target) != sha256.Size {
		return "", fmt.Errorf("Challenge-Ziel hat %d statt %d Bytes", len(target), sha256.Size)
	}

	buf := make([]byte, len(prefix), len(prefix)+20)
	copy(buf, prefix)
	prefixLen := len(buf)

	for nonce := uint64(0); ; nonce++ {
		// Checking the context on every iteration would dominate the hash
		// function for small inputs, so do it in reasonably sized batches.
		if nonce&0x3FFF == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}

		buf = strconv.AppendUint(buf[:prefixLen], nonce, 10)
		sum := sha256.Sum256(buf)
		if bytes.Compare(sum[:], target) <= 0 {
			return strconv.FormatUint(nonce, 10), nil
		}
	}
}
