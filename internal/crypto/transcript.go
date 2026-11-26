// Application transcripts bind the RFC primitives to the exact PoML attempt.
package crypto

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"

	"github.com/thomas/poml-node/internal/types"
	"github.com/thomas/poml-node/profiles"
)

// ExpandRandomness supplies every ordered model slot from the public inference VRF.
func ExpandRandomness(spec profiles.Spec, attemptInput, rho []byte) ([][]byte, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if len(rho) != 64 {
		return nil, errors.New("invalid inference VRF output")
	}
	transcript := make([][]byte, spec.RandomnessSlots)
	prk, err := hkdf.Extract(sha256.New, rho, nil)
	if err != nil {
		return nil, err
	}
	digest := spec.Digest()
	for i := range transcript {
		info := types.HashBytes("randomness-slot", digest[:], attemptInput, binary.BigEndian.AppendUint32(nil, uint32(i)))
		transcript[i], err = hkdf.Expand(sha256.New, prk, string(info[:]), int(spec.RandomnessBytes))
		if err != nil {
			return nil, err
		}
	}
	return transcript, nil
}

func encryptionContext(chainID string, w types.Winner) (salt, info, aad types.Hash) {
	profile, parent, query, attempt := []byte(w.ModelID), w.Parent[:], []byte(w.QueryID), []byte(w.AttemptID)
	return types.HashBytes("encryption-seed", profile, parent, query, attempt),
		types.HashBytes("hpke-info", profile, parent, query, attempt),
		types.HashBytes("hpke-aad", []byte(chainID), profile, parent, query, attempt)
}

// SealOutput encodes the full useful output and query ID, then uses the private
// encryption VRF result as prescribed coins for a single HPKE message.
func SealOutput(recipient, coins []byte, chainID string, w types.Winner, output []byte) ([]byte, error) {
	if len(coins) != 64 {
		return nil, errors.New("invalid encryption VRF output")
	}
	salt, info, aad := encryptionContext(chainID, w)
	seed, err := hkdf.Extract(sha256.New, coins, salt[:])
	if err != nil {
		return nil, err
	}
	plain := binary.BigEndian.AppendUint64(nil, uint64(len(output)))
	plain = append(plain, output...)
	plain = append(plain, w.QueryID...)
	return sealWithSeed(recipient, seed, info[:], aad[:], plain)
}

// OpenOutput authenticates chain and attempt metadata and returns the user output.
func OpenOutput(private []byte, chainID string, w types.Winner) ([]byte, error) {
	_, info, aad := encryptionContext(chainID, w)
	plain, err := DecryptDeterministic(private, info[:], aad[:], w.Ciphertext)
	if err != nil {
		return nil, err
	}
	if len(plain) < 8 {
		return nil, errors.New("invalid output framing")
	}
	size := binary.BigEndian.Uint64(plain[:8])
	if size > uint64(len(plain)-8) || !bytes.Equal(plain[8+size:], []byte(w.QueryID)) {
		return nil, errors.New("invalid output size or query ID")
	}
	return plain[8 : 8+size], nil
}
