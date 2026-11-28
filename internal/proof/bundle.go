// Package proof defines the consensus-facing proof envelope.
//
// The envelope is deliberately independent of a proving backend. A model
// prover can place an EZKL proof, a DeepProve proof, or a composed receipt in
// ModelProof. BindingProof carries the proof that connects that model proof to
// the PoML statement. Nodes verify the envelope and its statement digest
// before applying a winner.
package proof

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/types"
)

const (
	formatVersion      = byte(1)
	maxBundleSize      = 16 << 20
	maxPartSize        = 16 << 20
	modelFormatVersion = byte(1)
)

// Bundle is the canonical proof object carried in a winner block.
type Bundle struct {
	ProfileID     string
	SetupDigest   types.Hash
	StatementHash types.Hash
	ModelProof    []byte
	BindingProof  []byte
}

// ModelProof is the backend-specific model proof carried inside a bundle.
// PublicInstance is a canonical serialization of the proof backend's public
// instances. It is signed by the auxiliary binding proof so the model proof
// and the PoML ciphertext cannot be swapped independently.
type ModelProof struct {
	Backend        string
	PublicInstance []byte
	Proof          []byte
}

// BindingStatement returns the exact bytes signed by the auxiliary proof
// worker. Legacy profiles sign only statement. Model-proof profiles append a
// length-delimited public instance to bind the two proof layers together.
func BindingStatement(statement, publicInstance []byte) []byte {
	if len(publicInstance) == 0 {
		return append([]byte(nil), statement...)
	}
	var output bytes.Buffer
	output.WriteString("poml-proof-binding/v1")
	writeBytes(&output, statement)
	writeBytes(&output, publicInstance)
	return output.Bytes()
}

// EncodeModelProof creates the bounded envelope used inside Bundle.ModelProof.
func EncodeModelProof(model ModelProof) ([]byte, error) {
	if model.Backend == "" || len(model.PublicInstance) == 0 || len(model.Proof) == 0 {
		return nil, errors.New("model proof is incomplete")
	}
	if len(model.Backend) > maxPartSize || len(model.PublicInstance) > maxPartSize || len(model.Proof) > maxPartSize {
		return nil, errors.New("model proof part is too large")
	}
	var output bytes.Buffer
	output.WriteString("PMLP")
	output.WriteByte(modelFormatVersion)
	writeBytes(&output, []byte(model.Backend))
	writeBytes(&output, model.PublicInstance)
	writeBytes(&output, model.Proof)
	if output.Len() > maxBundleSize {
		return nil, errors.New("model proof is too large")
	}
	return output.Bytes(), nil
}

// DecodeModelProof parses the backend envelope. Backend verification is kept
// in a separate verifier because EZKL and future proof systems have different
// runtimes and setup artifacts.
func DecodeModelProof(encoded []byte) (ModelProof, error) {
	if len(encoded) == 0 || len(encoded) > maxBundleSize {
		return ModelProof{}, errors.New("invalid model proof size")
	}
	reader := &bundleReader{data: encoded}
	if string(reader.take(4)) != "PMLP" || reader.byte() != modelFormatVersion {
		return ModelProof{}, errors.New("unsupported model proof version")
	}
	backend := reader.bytes()
	instance := reader.bytes()
	proof := reader.bytes()
	if reader.err != nil {
		return ModelProof{}, reader.err
	}
	if reader.pos != len(encoded) || len(backend) == 0 || len(instance) == 0 || len(proof) == 0 {
		return ModelProof{}, errors.New("invalid model proof envelope")
	}
	return ModelProof{Backend: string(backend), PublicInstance: instance, Proof: proof}, nil
}

// StatementDigest returns the domain-separated digest used by every proof
// backend. The statement itself remains in the signed winner bytes.
func StatementDigest(statement []byte) types.Hash {
	return types.HashBytes("proof-statement", statement)
}

// Encode serializes a bundle with fixed-width lengths and no map ordering.
func Encode(bundle Bundle) ([]byte, error) {
	if bundle.ProfileID == "" || len(bundle.ModelProof) == 0 || len(bundle.BindingProof) == 0 {
		return nil, errors.New("proof bundle is incomplete")
	}
	if len(bundle.ProfileID) > maxPartSize || len(bundle.ModelProof) > maxPartSize || len(bundle.BindingProof) > maxPartSize {
		return nil, errors.New("proof bundle part is too large")
	}
	var output bytes.Buffer
	output.WriteString("PMLB")
	output.WriteByte(formatVersion)
	writeBytes(&output, []byte(bundle.ProfileID))
	output.Write(bundle.SetupDigest[:])
	output.Write(bundle.StatementHash[:])
	writeBytes(&output, bundle.ModelProof)
	writeBytes(&output, bundle.BindingProof)
	if output.Len() > maxBundleSize {
		return nil, errors.New("proof bundle is too large")
	}
	return output.Bytes(), nil
}

// Decode parses and validates only the envelope. Backend-specific proof
// validation happens in VerifyBundle or in the configured proof verifier.
func Decode(encoded []byte) (Bundle, error) {
	if len(encoded) == 0 || len(encoded) > maxBundleSize {
		return Bundle{}, errors.New("invalid proof bundle size")
	}
	reader := &bundleReader{data: encoded}
	if string(reader.take(4)) != "PMLB" || reader.byte() != formatVersion {
		return Bundle{}, errors.New("unsupported proof bundle version")
	}
	profile := reader.bytes()
	if len(profile) == 0 || len(profile) > maxPartSize {
		return Bundle{}, errors.New("invalid proof profile")
	}
	setup := reader.hash()
	statement := reader.hash()
	modelProof := reader.bytes()
	bindingProof := reader.bytes()
	if reader.err != nil {
		return Bundle{}, reader.err
	}
	if reader.pos != len(encoded) {
		return Bundle{}, errors.New("trailing bytes in proof bundle")
	}
	if len(modelProof) == 0 || len(bindingProof) == 0 {
		return Bundle{}, errors.New("proof bundle contains an empty proof")
	}
	return Bundle{ProfileID: string(profile), SetupDigest: setup, StatementHash: statement,
		ModelProof: modelProof, BindingProof: bindingProof}, nil
}

// VerifyBundle checks the common certificate-backed profile. A ZK model proof
// is verified through the configured backend hook after its binding
// certificate passes. The envelope and statement bytes remain backend-neutral.
func VerifyBundle(encoded []byte, expectedProfile string, expectedSetup types.Hash, statement []byte, certificatePublic []byte, verifiers ...ModelVerifier) (Bundle, error) {
	bundle, err := Decode(encoded)
	if err != nil {
		return Bundle{}, err
	}
	if bundle.ProfileID != expectedProfile || bundle.SetupDigest != expectedSetup {
		return Bundle{}, errors.New("proof profile or setup digest mismatch")
	}
	if bundle.StatementHash != StatementDigest(statement) {
		return Bundle{}, errors.New("proof statement digest mismatch")
	}
	publicInstance := []byte(nil)
	var model ModelProof
	if strings.HasPrefix(expectedProfile, "zk-") {
		model, err = DecodeModelProof(bundle.ModelProof)
		if err != nil {
			return Bundle{}, fmt.Errorf("model proof envelope: %w", err)
		}
		if expectedBackend := backendForProfile(expectedProfile); expectedBackend != "" && model.Backend != expectedBackend {
			return Bundle{}, fmt.Errorf("model proof backend %q does not match %q", model.Backend, expectedBackend)
		}
		publicInstance = model.PublicInstance
		if len(verifiers) != 1 || verifiers[0] == nil {
			return Bundle{}, errors.New("model proof verifier is unavailable")
		}
	}
	if err := cryptoutil.Verify(certificatePublic, bundle.BindingProof, "certificate", BindingStatement(statement, publicInstance)); err != nil {
		return Bundle{}, fmt.Errorf("binding proof: %w", err)
	}
	if strings.HasPrefix(expectedProfile, "zk-") {
		// Verify the certificate before invoking an external verifier. A peer
		// must not spend proof-verification resources on an unsigned bundle.
		if err := verifiers[0].Verify(expectedProfile, expectedSetup, model); err != nil {
			return Bundle{}, fmt.Errorf("model proof verification: %w", err)
		}
	}
	return bundle, nil
}

func backendForProfile(profile string) string {
	switch {
	case strings.HasPrefix(profile, "zk-ezkl-"):
		return "ezkl"
	case strings.HasPrefix(profile, "zk-deepprove-"):
		return "deepprove"
	default:
		return ""
	}
}

func writeBytes(output *bytes.Buffer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	output.Write(length[:])
	output.Write(value)
}

type bundleReader struct {
	data []byte
	pos  int
	err  error
}

func (reader *bundleReader) take(size int) []byte {
	if reader.err != nil || size < 0 || size > len(reader.data)-reader.pos {
		reader.err = errors.New("truncated proof bundle")
		return nil
	}
	value := reader.data[reader.pos : reader.pos+size]
	reader.pos += size
	return value
}

func (reader *bundleReader) byte() byte {
	value := reader.take(1)
	if len(value) == 0 {
		return 0
	}
	return value[0]
}

func (reader *bundleReader) bytes() []byte {
	lengthBytes := reader.take(8)
	if reader.err != nil {
		return nil
	}
	length := binary.BigEndian.Uint64(lengthBytes)
	if length > maxPartSize || length > uint64(len(reader.data)-reader.pos) {
		reader.err = errors.New("proof bundle field is too large")
		return nil
	}
	return reader.take(int(length))
}

func (reader *bundleReader) hash() types.Hash {
	var value types.Hash
	data := reader.take(len(value))
	if reader.err == nil {
		copy(value[:], data)
	}
	return value
}
