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

// CommitmentNonce derives the private hiding nonce from the secret encryption
// VRF output and the attempt context. It is deterministic for challenge
// replay, but unpredictable to peers because the VRF output stays private.
func CommitmentNonce(chainID string, w types.Winner, coins []byte) ([]byte, error) {
	if len(coins) != 64 {
		return nil, errors.New("invalid encryption VRF output")
	}
	salt := types.HashBytes("output-commitment-salt", []byte(chainID), []byte(w.ModelID), w.Parent[:], w.PrefixDigest[:], []byte(w.QueryID), []byte(w.AttemptID))
	prk, err := hkdf.Extract(sha256.New, coins, salt[:])
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "output-commitment-nonce", 32)
	if err != nil {
		return nil, err
	}
	return nonce, nil
}

// OutputCommitment is the public, hiding commitment used by the Stage 2
// proof bundle. The nonce is kept in the private witness and encrypted payload.
func OutputCommitment(nonce, output []byte) types.Hash {
	return types.HashBytes("output-commitment", nonce, output)
}

// SealOutputBound is the versioned payload format for proof-bearing profiles.
// It carries the commitment nonce so the recipient can recompute the public
// commitment after decryption. The legacy SealOutput format remains available
// to the explicit dummy profile.
func SealOutputBound(recipient, coins []byte, chainID string, w types.Winner, nonce, output []byte) ([]byte, error) {
	if len(nonce) != 32 {
		return nil, errors.New("invalid output commitment nonce")
	}
	if len(coins) != 64 {
		return nil, errors.New("invalid encryption VRF output")
	}
	salt, info, aad := encryptionContext(chainID, w)
	seed, err := hkdf.Extract(sha256.New, coins, salt[:])
	if err != nil {
		return nil, err
	}
	plain := []byte{1}
	plain = binary.BigEndian.AppendUint64(plain, uint64(len(nonce)))
	plain = append(plain, nonce...)
	plain = binary.BigEndian.AppendUint64(plain, uint64(len(output)))
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

// OpenOutputBound opens the proof-bearing payload and returns its private
// commitment nonce together with the output.
func OpenOutputBound(private []byte, chainID string, w types.Winner) ([]byte, []byte, error) {
	_, info, aad := encryptionContext(chainID, w)
	plain, err := DecryptDeterministic(private, info[:], aad[:], w.Ciphertext)
	if err != nil {
		return nil, nil, err
	}
	if len(plain) < 1+8+32+8 || plain[0] != 1 {
		return nil, nil, errors.New("invalid bound output framing")
	}
	nonceSize := binary.BigEndian.Uint64(plain[1:9])
	if nonceSize != 32 || nonceSize > uint64(len(plain)-9) {
		return nil, nil, errors.New("invalid commitment nonce size")
	}
	nonceStart := 9
	nonceEnd := nonceStart + int(nonceSize)
	if nonceEnd+8 > len(plain) {
		return nil, nil, errors.New("truncated bound output")
	}
	outputSize := binary.BigEndian.Uint64(plain[nonceEnd : nonceEnd+8])
	outputStart := nonceEnd + 8
	outputEnd := uint64(outputStart) + outputSize
	if outputEnd > uint64(len(plain)) || outputEnd+uint64(len(w.QueryID)) != uint64(len(plain)) || !bytes.Equal(plain[outputEnd:], []byte(w.QueryID)) {
		return nil, nil, errors.New("invalid bound output size or query ID")
	}
	return append([]byte(nil), plain[outputStart:outputEnd]...), append([]byte(nil), plain[nonceStart:nonceEnd]...), nil
}
