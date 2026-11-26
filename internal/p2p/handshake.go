// The signed transcript covers a fresh nonce, both public keys, the shared
// genesis, protocol capabilities, profile and advertised listening addresses.
package p2p

import (
	"bytes"
	"crypto/rand"
	"errors"
	"time"

	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/p2p/wire"
	"github.com/thomas/poml-node/internal/types"
)

func (p *peer) handshake(dialed string) error {
	cfg := p.server.config
	if err := p.conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ours, err := frame(wire.Frame_HELLO, &wire.Hello{Version: 1, Capability: capability, Chain: cfg.ChainID, Profile: cfg.ProfileID, Genesis: cfg.GenesisHash[:], PublicKey: cfg.IdentityPub, Name: cfg.NodeID, Address: cfg.Advertise, Nonce: nonce})
	if err != nil {
		return err
	}
	if err = writeFrame(p.conn, ours); err != nil {
		return err
	}
	theirs, err := expect(p.conn, wire.Frame_HELLO)
	if err != nil {
		return err
	}
	var hello wire.Hello
	if err := decode(theirs, &hello); err != nil {
		return err
	}
	if hello.Version != 1 || hello.Capability != capability || hello.Chain != cfg.ChainID || hello.Profile != cfg.ProfileID || !bytes.Equal(hello.Genesis, cfg.GenesisHash[:]) {
		return errors.New("peer protocol, genesis or active profile mismatch")
	}
	if !validAddress(hello.Address) || hello.Name == "" || len(hello.Name) > 128 || len(hello.Nonce) != 32 || bytes.Equal(hello.PublicKey, cfg.IdentityPub) {
		return errors.New("invalid peer identity or advertised endpoint")
	}
	ack := cryptoutil.Sign(cfg.Identity, "peer-ack", transcript(ours.Payload, theirs))
	if err := writeFrame(p.conn, raw(wire.Frame_ACK, ack)); err != nil {
		return err
	}
	remoteAck, err := expect(p.conn, wire.Frame_ACK)
	if err != nil {
		return err
	}
	if err := cryptoutil.Verify(hello.PublicKey, remoteAck, "peer-ack", transcript(theirs, ours.Payload)); err != nil {
		return err
	}
	p.public = hello.PublicKey
	p.remote = PeerInfo{ID: types.HashBytes("peer-key", hello.PublicKey).String(), Name: hello.Name, Address: hello.Address, Outbound: dialed != ""}
	return p.server.learn([]string{hello.Address})
}
