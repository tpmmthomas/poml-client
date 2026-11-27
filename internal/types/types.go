// Package types contains the immutable protocol objects shared by consensus,
// storage, networking and the experiment tools.
package types

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

const HashSize = 32

// Hash is the fixed-width digest used by the Stage 1 protocol.
type Hash [HashSize]byte

func (h Hash) String() string { return hex.EncodeToString(h[:]) }

func ParseHash(value string) (Hash, error) {
	var h Hash
	b, err := hex.DecodeString(value)
	if err != nil || len(b) != HashSize {
		return h, fmt.Errorf("hash must be %d hexadecimal bytes", HashSize)
	}
	copy(h[:], b)
	return h, nil
}

func HashBytes(domain string, parts ...[]byte) Hash {
	h := sha256.New()
	h.Write([]byte("poml-stage1/v1/" + domain + "/"))
	for _, part := range parts {
		var length [8]byte
		for i := range length {
			length[7-i] = byte(len(part) >> (8 * i))
		}
		h.Write(length[:])
		h.Write(part)
	}
	var result Hash
	copy(result[:], h.Sum(nil))
	return result
}

type TxKind uint8

const (
	TxQuery TxKind = iota + 1
	TxTransfer
	TxResponse
	TxChallenge
	TxChallengeAnswer
	TxMinerRegistration
)

func (k TxKind) String() string {
	switch k {
	case TxQuery:
		return "query"
	case TxTransfer:
		return "transfer"
	case TxResponse:
		return "response"
	case TxChallenge:
		return "challenge"
	case TxChallengeAnswer:
		return "challenge_answer"
	case TxMinerRegistration:
		return "miner_registration"
	default:
		return "unknown"
	}
}

var ErrInvalidObject = errors.New("invalid protocol object")

type Tx struct {
	Kind                TxKind
	Sender              string
	Nonce               uint64
	QueryID             string
	Input               []byte
	Commitment          Hash
	Recipient           []byte
	ModelID             string
	Expiry              uint64
	Amount              uint64
	Fee                 uint64
	Parent              Hash
	PrefixDigest        Hash
	AttemptID           string
	MinerID             string
	CreatedAt           uint64
	CompletionSequence  uint64
	Rho                 []byte
	RhoProof            []byte
	CTicket             uint64
	Ciphertext          []byte
	TicketHash          Hash
	ProofStatement      []byte
	Proof               []byte
	IdentityPublic      []byte
	InferenceVRFPublic  []byte
	EncryptionVRFPublic []byte
	Signature           []byte
}

type Miner struct {
	ID               string
	IdentityPub      []byte
	InferenceVRFPub  []byte
	EncryptionVRFPub []byte
	Bond             uint64
	LockedBond       uint64
	RegisteredAt     uint64
	Active           bool
}

type Account struct {
	Balance uint64
	Nonce   uint64
	Public  []byte
}

type Query struct {
	ID         string
	Sender     string
	Input      []byte
	Commitment Hash
	Recipient  []byte
	ModelID    string
	Fee        uint64
	Expiry     uint64
	CreatedAt  uint64
	Settled    bool
	Refunded   bool
	Expired    bool
	Escrow     uint64
	SignedTx   []byte
}

// ClaimStatus is the explicit lifecycle state of an optimistic response.
// Boolean fields on Claim are retained for wire/backward compatibility and
// mirror the terminal states where applicable.
type ClaimStatus uint8

const (
	ClaimPending ClaimStatus = iota + 1
	ClaimChallenged
	ClaimAnswered
	ClaimSettled
	ClaimExpired
	ClaimSlashed
	ClaimReopened
)

type Claim struct {
	QueryID        string
	MinerID        string
	Amount         uint64
	BlockHeight    uint64
	Deadline       uint64
	Challenged     bool
	Settled        bool
	Status         ClaimStatus
	AttemptID      string
	Parent         Hash
	PrefixDigest   Hash
	Ciphertext     []byte
	TicketHash     Hash
	Rho            []byte
	RhoProof       []byte
	CTicket        uint64
	Challenger     string
	ChallengeFee   uint64
	AnswerDeadline uint64
	BondLocked     uint64
	ProofStatement []byte
	Proof          []byte
	Refunded       bool
	Slashed        bool
}

type Winner struct {
	QueryID             string
	AttemptID           string
	MinerID             string
	ModelID             string
	ChainID             string
	ProfileDigest       Hash
	ProofProfile        string
	ProofSetupDigest    Hash
	InputCommitment     Hash
	Recipient           []byte
	InferenceVRFPublic  []byte
	EncryptionVRFPublic []byte
	EncryptionVRFInput  []byte
	Parent              Hash
	PrefixDigest        Hash
	Rho                 []byte
	RhoProof            []byte
	Ciphertext          []byte
	OutputCommitment    Hash
	CTicket             uint64
	TicketHash          Hash
	ProofBundle         []byte
	Certificate         []byte
}

type Header struct {
	Parent       Hash
	Height       uint64
	Producer     string
	Timestamp    int64
	PrefixDigest Hash
	ClaimDigest  Hash
	BodyDigest   Hash
	TicketHash   Hash
}

type Block struct {
	Header    Header
	Prefix    []Tx
	Responses []Tx
	Winner    Winner
	Signature []byte
}

type State struct {
	Height   uint64
	Accounts map[string]Account
	Miners   map[string]Miner
	Queries  map[string]Query
	Claims   map[string]Claim
}

func NewState() State {
	return State{
		Accounts: make(map[string]Account),
		Miners:   make(map[string]Miner),
		Queries:  make(map[string]Query),
		Claims:   make(map[string]Claim),
	}
}

func (s State) Copy() State {
	copyState := NewState()
	copyState.Height = s.Height
	for key, value := range s.Accounts {
		value.Public = append([]byte(nil), value.Public...)
		copyState.Accounts[key] = value
	}
	for key, value := range s.Miners {
		value.IdentityPub = append([]byte(nil), value.IdentityPub...)
		value.InferenceVRFPub = append([]byte(nil), value.InferenceVRFPub...)
		value.EncryptionVRFPub = append([]byte(nil), value.EncryptionVRFPub...)
		copyState.Miners[key] = value
	}
	for key, value := range s.Queries {
		value.Input = append([]byte(nil), value.Input...)
		value.Recipient = append([]byte(nil), value.Recipient...)
		value.SignedTx = append([]byte(nil), value.SignedTx...)
		copyState.Queries[key] = value
	}
	for key, value := range s.Claims {
		value.Ciphertext = append([]byte(nil), value.Ciphertext...)
		value.Rho = append([]byte(nil), value.Rho...)
		value.RhoProof = append([]byte(nil), value.RhoProof...)
		value.ProofStatement = append([]byte(nil), value.ProofStatement...)
		value.Proof = append([]byte(nil), value.Proof...)
		copyState.Claims[key] = value
	}
	return copyState
}
