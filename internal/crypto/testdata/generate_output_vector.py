#!/usr/bin/env python3
"""Reproduce poml-output.json independently with Python's cryptography package."""

import hashlib
import hmac
import json
import struct

from cryptography.hazmat.primitives.asymmetric import x25519
from cryptography.hazmat.primitives.ciphers.aead import AESGCM
from cryptography.hazmat.primitives.serialization import Encoding, PublicFormat

KEM_SUITE = b"KEM\x00\x20"
HPKE_SUITE = b"HPKE\x00\x20\x00\x01\x00\x02"


def tag(domain, *parts):
    encoded = b"".join(struct.pack(">Q", len(part)) + part for part in parts)
    return hashlib.sha256(b"poml-stage1/v1/" + domain + b"/" + encoded).digest()


def extract(salt, material):
    return hmac.digest(salt or bytes(32), material, "sha256")


def labeled_extract(suite, salt, label, material):
    return extract(salt, b"HPKE-v1" + suite + label + material)


def labeled_expand(suite, key, label, info, size):
    # All lengths in this fixed test suite fit in one HKDF-SHA256 output block.
    context = struct.pack(">H", size) + b"HPKE-v1" + suite + label + info
    return hmac.digest(key, context + b"\x01", "sha256")[:size]


def public_bytes(key):
    return key.public_bytes(Encoding.Raw, PublicFormat.Raw)


def vector():
    profile, parent, query, attempt = b"fixture-a", bytes(range(32)), b"example-q", b"miner-2-7"
    chain, output, coins = b"example-chain", b"complete example", bytes(range(64))
    recipient_secret = bytes(range(10, 42))
    recipient = x25519.X25519PrivateKey.from_private_bytes(recipient_secret).public_key()

    fields = profile, parent, query, attempt
    seed = extract(tag(b"encryption-seed", *fields), coins)
    dkp_prk = labeled_extract(KEM_SUITE, b"", b"dkp_prk", seed)
    key_bytes = labeled_expand(KEM_SUITE, dkp_prk, b"sk", b"", 32)
    ephemeral = x25519.X25519PrivateKey.from_private_bytes(key_bytes)
    enc = public_bytes(ephemeral.public_key())
    eae_prk = labeled_extract(KEM_SUITE, b"", b"eae_prk", ephemeral.exchange(recipient))
    shared = labeled_expand(KEM_SUITE, eae_prk, b"shared_secret", enc + public_bytes(recipient), 32)

    info = tag(b"hpke-info", *fields)
    aad = tag(b"hpke-aad", chain, *fields)
    psk_hash = labeled_extract(HPKE_SUITE, b"", b"psk_id_hash", b"")
    info_hash = labeled_extract(HPKE_SUITE, b"", b"info_hash", info)
    context = b"\x00" + psk_hash + info_hash
    secret = labeled_extract(HPKE_SUITE, shared, b"secret", b"")
    key = labeled_expand(HPKE_SUITE, secret, b"key", context, 32)
    nonce = labeled_expand(HPKE_SUITE, secret, b"base_nonce", context, 12)
    plaintext = struct.pack(">Q", len(output)) + output + query
    ciphertext = enc + AESGCM(key).encrypt(nonce, plaintext, aad)

    return {
        "profile": profile.decode(),
        "parent": parent.hex(),
        "qid": query.decode(),
        "attempt_id": attempt.decode(),
        "chain_id": chain.decode(),
        "encryption_vrf": coins.hex(),
        "recipient_private": recipient_secret.hex(),
        "recipient_public": public_bytes(recipient).hex(),
        "output": output.hex(),
        "ciphertext": ciphertext.hex(),
    }


if __name__ == "__main__":
    print(json.dumps(vector(), indent=2))
