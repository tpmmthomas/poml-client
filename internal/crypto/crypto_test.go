package crypto

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"testing"
)

func TestVRFRoundTrip(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	output, proof, public, err := VRFProve(seed, []byte("attempt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := VRFVerify(public, []byte("attempt"), output, proof); err != nil {
		t.Fatal(err)
	}
	if err := VRFVerify(public, []byte("different"), output, proof); err == nil {
		t.Fatal("changed input unexpectedly verified")
	}
}

func TestDeterministicHPKERoundTrip(t *testing.T) {
	recipient, err := ecdh.X25519().GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	coins := []byte("fixed encryption VRF output")
	info := []byte("attempt context")
	aad := []byte("public statement")
	plaintext := []byte("fixture output")
	first, err := sealWithSeed(recipient.PublicKey().Bytes(), coins, info, aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sealWithSeed(recipient.PublicKey().Bytes(), coins, info, aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("prescribed coins produced different ciphertexts")
	}
	opened, err := DecryptDeterministic(recipient.Bytes(), info, aad, first)
	if err != nil {
		t.Fatal(err)
	}
	if string(opened) != string(plaintext) {
		t.Fatalf("opened %q, want %q", opened, plaintext)
	}
	if _, err := DecryptDeterministic(recipient.Bytes(), info, []byte("changed"), first); err == nil {
		t.Fatal("changed AAD unexpectedly decrypted")
	}
}

// A byte string signed for a worker certificate cannot be replayed as a transaction.
func TestSignatureDomains(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	payload := []byte("same bytes")
	signature := Sign(key, "certificate", payload)
	public := key.Public().(ed25519.PublicKey)
	if err := Verify(public, signature, "certificate", payload); err != nil {
		t.Fatal(err)
	}
	if err := Verify(public, signature, "transaction", payload); err == nil {
		t.Fatal("signature crossed protocol domains")
	}
}
