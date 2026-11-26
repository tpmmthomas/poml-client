// Package crypto contains the protocol's standard cryptographic building
// blocks. Consensus code calls these small functions instead of depending on
// library-specific key representations.
package crypto

import (
	"crypto/ed25519"

	"encoding/hex"
	"errors"
	"fmt"

	"github.com/tmc/vrf/rfc9381"
)

func DecodeHex(value string, size int) ([]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || (size >= 0 && len(decoded) != size) {
		return nil, fmt.Errorf("expected %d hexadecimal bytes", size)
	}
	return decoded, nil
}

// Sign scopes transaction, block, handshake and fixture signatures to this protocol version.
func Sign(privateKey ed25519.PrivateKey, domain string, message []byte) []byte {
	return ed25519.Sign(privateKey, append([]byte("poml-stage1/v1/"+domain+"/"), message...))
}

func Verify(publicKey, signature []byte, domain string, message []byte) error {
	if len(publicKey) != ed25519.PublicKeySize || len(signature) != ed25519.SignatureSize {
		return errors.New("invalid Ed25519 key or signature length")
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), append([]byte("poml-stage1/v1/"+domain+"/"), message...), signature) {
		return errors.New("signature verification failed")
	}
	return nil
}

func VRFProve(seed, input []byte) (output, proof, public []byte, err error) {
	if len(seed) != rfc9381.SeedSize {
		return nil, nil, nil, errors.New("VRF seed has the wrong length")
	}
	privateKey := rfc9381.NewKeyFromSeed(seed)
	vrfProof, err := privateKey.Prove(input)
	if err != nil {
		return nil, nil, nil, err
	}
	vrfOutput, err := vrfProof.Hash()
	if err != nil {
		return nil, nil, nil, err
	}
	publicKey := privateKey.PublicKey()
	return append([]byte(nil), vrfOutput[:]...), append([]byte(nil), vrfProof[:]...), append([]byte(nil), publicKey[:]...), nil
}

func VRFVerify(public, input, output, proof []byte) error {
	if len(public) != rfc9381.PublicKeySize || len(output) != rfc9381.OutputSize || len(proof) != rfc9381.ProofSize {
		return errors.New("invalid VRF byte lengths")
	}
	publicKey, err := rfc9381.ParsePublicKey(public)
	if err != nil {
		return err
	}
	vrfProof, err := rfc9381.ParseProof(proof)
	if err != nil {
		return err
	}
	verified, err := rfc9381.Verify(publicKey, input, vrfProof)
	if err != nil {
		return err
	}
	if string(verified[:]) != string(output) {
		return errors.New("VRF output does not match proof")
	}
	return nil
}
