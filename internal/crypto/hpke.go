package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hpke"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// This is the RFC 9180 DHKEM(X25519, HKDF-SHA256) profile. Go's high-level
// crypto/hpke sender API creates its own ephemeral key. PoML needs the
// ephemeral key derived from the encryption VRF output, so this file performs
// the RFC's labeled KEM and key-schedule steps over Go's standard primitives.
const (
	hpkeVersion = "HPKE-v1"
	kemID       = "\x00\x20"
	hpkeSuite   = "HPKE\x00\x20\x00\x01\x00\x02"
	keySize     = 32
	nonceSize   = 12
	encSize     = 32
)

func labeledExtract(salt []byte, suite, label string, ikm []byte) ([]byte, error) {
	input := append([]byte(hpkeVersion), []byte(suite)...)
	input = append(input, []byte(label)...)
	input = append(input, ikm...)
	return hkdf.Extract(sha256.New, input, salt)
}

func labeledExpand(prk []byte, suite, label string, info []byte, length int) ([]byte, error) {
	var encodedLength [2]byte
	if length > 65535 {
		return nil, errors.New("HPKE output is too large")
	}
	binary.BigEndian.PutUint16(encodedLength[:], uint16(length))
	context := append(encodedLength[:], []byte(hpkeVersion)...)
	context = append(context, []byte(suite)...)
	context = append(context, []byte(label)...)
	context = append(context, info...)
	return hkdf.Expand(sha256.New, prk, string(context), length)
}

func kemExtractAndExpand(dh, context []byte) ([]byte, error) {
	eaePrk, err := labeledExtract(nil, "KEM"+kemID, "eae_prk", dh)
	if err != nil {
		return nil, err
	}
	return labeledExpand(eaePrk, "KEM"+kemID, "shared_secret", context, keySize)
}

func keySchedule(sharedSecret, info []byte) (key, nonce []byte, err error) {
	pskIDHash, err := labeledExtract(nil, hpkeSuite, "psk_id_hash", nil)
	if err != nil {
		return nil, nil, err
	}
	infoHash, err := labeledExtract(nil, hpkeSuite, "info_hash", info)
	if err != nil {
		return nil, nil, err
	}
	context := append([]byte{0}, pskIDHash...)
	context = append(context, infoHash...)
	secret, err := labeledExtract(sharedSecret, hpkeSuite, "secret", nil)
	if err != nil {
		return nil, nil, err
	}
	key, err = labeledExpand(secret, hpkeSuite, "key", context, keySize)
	if err != nil {
		return nil, nil, err
	}
	nonce, err = labeledExpand(secret, hpkeSuite, "base_nonce", context, nonceSize)
	return key, nonce, err
}

func aead(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealWithSeed is the single-shot Base sender with deterministic X25519 keys.
func sealWithSeed(recipientPublic, seed, info, aad, plaintext []byte) ([]byte, error) {
	derived, err := hpke.DHKEM(ecdh.X25519()).DeriveKeyPair(seed)
	if err != nil {
		return nil, err
	}
	keyBytes, err := derived.Bytes()
	if err != nil {
		return nil, err
	}
	ephemeral, err := ecdh.X25519().NewPrivateKey(keyBytes)
	if err != nil {
		return nil, err
	}
	recipient, err := ecdh.X25519().NewPublicKey(recipientPublic)
	if err != nil {
		return nil, err
	}
	dh, err := ephemeral.ECDH(recipient)
	if err != nil {
		return nil, err
	}
	enc := ephemeral.PublicKey().Bytes()
	context := append(append([]byte(nil), enc...), recipient.Bytes()...)
	shared, err := kemExtractAndExpand(dh, context)
	if err != nil {
		return nil, err
	}
	key, nonce, err := keySchedule(shared, info)
	if err != nil {
		return nil, err
	}
	box, err := aead(key)
	if err != nil {
		return nil, err
	}
	ciphertext := box.Seal(nil, nonce, plaintext, aad)
	return append(enc, ciphertext...), nil
}

// DecryptDeterministic opens the prescribed-coin sender with Go's RFC 9180 recipient.
func DecryptDeterministic(recipientPrivate, info, aad, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < encSize+16 {
		return nil, errors.New("HPKE ciphertext is too short")
	}
	key, err := hpke.DHKEM(ecdh.X25519()).NewPrivateKey(recipientPrivate)
	if err != nil {
		return nil, err
	}
	receiver, err := hpke.NewRecipient(ciphertext[:encSize], key, hpke.HKDFSHA256(), hpke.AES256GCM(), info)
	if err != nil {
		return nil, err
	}
	return receiver.Open(aad, ciphertext[encSize:])
}
