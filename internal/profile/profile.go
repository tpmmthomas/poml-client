// Package profile loads the versioned genesis and operator configuration.
package profile

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"

	"github.com/pelletier/go-toml/v2"
	"github.com/thomas/poml-node/internal/types"
	"github.com/thomas/poml-node/profiles"
)

type GenesisFile struct {
	Version              int           `json:"version"`
	ChainID              string        `json:"chain_id"`
	ProtocolVersion      uint32        `json:"protocol_version"`
	HashBits             uint32        `json:"hash_bits"`
	BaseTargetHex        string        `json:"base_target_hex"`
	ModelProfileID       string        `json:"model_profile_id"`
	Model                profiles.Spec `json:"model_profile"`
	CertificatePublicHex string        `json:"certificate_public_key_hex"`
	BlockReward          uint64        `json:"block_reward"`
	QueryFee             uint64        `json:"query_fee"`
	ResponseFee          uint64        `json:"response_fee"`
	ChallengeFee         uint64        `json:"challenge_fee"`
	ChallengeBounty      uint64        `json:"challenge_bounty"`
	ChallengeAnswerFee   uint64        `json:"challenge_answer_fee"`
	BondSlash            uint64        `json:"bond_slash"`
	AnswerWindow         uint64        `json:"answer_window"`
	ChallengeWindow      uint64        `json:"challenge_window"`
	ResponseLimit        uint64        `json:"response_limit"`
	MaxResponseBytes     uint64        `json:"max_response_bytes"`
	MaxValidationWork    uint64        `json:"max_validation_work"`
	MaxOrdinaryTxs       uint64        `json:"max_ordinary_txs"`
	MaxBlockBytes        uint64        `json:"max_block_bytes"`
	InitialAccounts      []AccountFile `json:"initial_accounts"`
	InitialMiners        []MinerFile   `json:"initial_miners"`
}

type AccountFile struct {
	ID           string `json:"id"`
	Balance      uint64 `json:"balance"`
	PublicKeyHex string `json:"public_key_hex"`
}

type MinerFile struct {
	ID                string `json:"id"`
	IdentityPublicHex string `json:"identity_public_key_hex"`
	InferenceVRFHex   string `json:"inference_vrf_public_key_hex"`
	EncryptionVRFHex  string `json:"encryption_vrf_public_key_hex"`
	Bond              uint64 `json:"bond"`
}

type Genesis struct {
	Raw               GenesisFile
	Hash              types.Hash
	BaseTarget        *big.Int
	CertificatePublic ed25519.PublicKey
	Accounts          map[string]types.Account
	Miners            map[string]types.Miner
}

func LoadGenesis(path string) (Genesis, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Genesis{}, err
	}
	var raw GenesisFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return Genesis{}, fmt.Errorf("decode genesis: %w", err)
	}
	if raw.Version != 1 || raw.ChainID == "" || raw.HashBits != 256 {
		return Genesis{}, errors.New("unsupported or incomplete genesis")
	}
	if raw.Model.ID == "" {
		model, modelErr := profiles.Load(raw.ModelProfileID)
		if modelErr != nil {
			return Genesis{}, fmt.Errorf("model profile: %w", modelErr)
		}
		raw.Model = model
	}
	if raw.Model.ID != raw.ModelProfileID {
		return Genesis{}, errors.New("model profile ID does not match model_profile_id")
	}
	if err := raw.Model.Validate(); err != nil {
		return Genesis{}, fmt.Errorf("model profile: %w", err)
	}
	baseTarget := new(big.Int)
	if _, ok := baseTarget.SetString(raw.BaseTargetHex, 16); !ok || baseTarget.Sign() <= 0 {
		return Genesis{}, errors.New("base_target_hex must be a positive hexadecimal integer")
	}
	cert, err := decodeHex(raw.CertificatePublicHex, ed25519.PublicKeySize)
	if err != nil {
		return Genesis{}, fmt.Errorf("certificate key: %w", err)
	}
	accounts := make(map[string]types.Account, len(raw.InitialAccounts))
	for _, item := range raw.InitialAccounts {
		if item.ID == "" || item.Balance == 0 {
			return Genesis{}, errors.New("genesis account needs an id and balance")
		}
		if _, exists := accounts[item.ID]; exists {
			return Genesis{}, fmt.Errorf("duplicate genesis account %s", item.ID)
		}
		public, err := decodeHex(item.PublicKeyHex, ed25519.PublicKeySize)
		if err != nil {
			return Genesis{}, fmt.Errorf("account %s key: %w", item.ID, err)
		}
		accounts[item.ID] = types.Account{Balance: item.Balance, Public: public}
	}
	miners := make(map[string]types.Miner, len(raw.InitialMiners))
	for _, item := range raw.InitialMiners {
		identity, identityErr := decodeHex(item.IdentityPublicHex, ed25519.PublicKeySize)
		inference, inferenceErr := decodeHex(item.InferenceVRFHex, 32)
		encryption, encryptionErr := decodeHex(item.EncryptionVRFHex, 32)
		if item.ID == "" || identityErr != nil || inferenceErr != nil || encryptionErr != nil {
			return Genesis{}, fmt.Errorf("miner %s has invalid key material", item.ID)
		}
		if _, exists := miners[item.ID]; exists {
			return Genesis{}, fmt.Errorf("duplicate genesis miner %s", item.ID)
		}
		miners[item.ID] = types.Miner{
			ID: item.ID, IdentityPub: identity, InferenceVRFPub: inference,
			EncryptionVRFPub: encryption, Bond: item.Bond, Active: true,
		}
	}
	normalized, err := json.Marshal(raw)
	if err != nil {
		return Genesis{}, err
	}
	return Genesis{
		Raw: raw, Hash: types.HashBytes("genesis", normalized), BaseTarget: baseTarget,
		CertificatePublic: ed25519.PublicKey(cert), Accounts: accounts, Miners: miners,
	}, nil
}

type NodeConfig struct {
	NodeID             string   `toml:"node_id"`
	Listen             string   `toml:"listen"`
	Advertise          string   `toml:"advertise"`
	Seeds              []string `toml:"seeds"`
	RPC                string   `toml:"rpc"`
	RPCToken           string   `toml:"rpc_token"`
	DataDir            string   `toml:"data_dir"`
	Genesis            string   `toml:"genesis"`
	WorkerSocket       string   `toml:"worker_socket"`
	CertificateSocket  string   `toml:"certificate_socket"`
	IdentityPrivateHex string   `toml:"identity_private_key_hex"`
	InferenceVRFHex    string   `toml:"inference_vrf_seed_hex"`
	EncryptionVRFHex   string   `toml:"encryption_vrf_seed_hex"`
	CertificatePrivHex string   `toml:"certificate_private_key_hex"`
	MinerID            string   `toml:"miner_id"`
	RunID              string   `toml:"run_id"`
	Mining             bool     `toml:"mining"`
	MaxAttempts        uint64   `toml:"max_attempts"`
	AttemptParallelism int      `toml:"attempt_parallelism"`
}

func LoadNodeConfig(path string) (NodeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return NodeConfig{}, err
	}
	var config NodeConfig
	if err := toml.Unmarshal(data, &config); err != nil {
		return NodeConfig{}, fmt.Errorf("decode node config: %w", err)
	}
	if config.NodeID == "" || config.Listen == "" || config.Genesis == "" || config.DataDir == "" {
		return NodeConfig{}, errors.New("node config requires node_id, listen, genesis and data_dir")
	}
	if config.RPC == "" {
		config.RPC = "127.0.0.1:0"
	}
	if config.Advertise == "" {
		config.Advertise = config.Listen
	}
	if config.RunID == "" {
		config.RunID = "local"
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = 1000
	}
	if config.AttemptParallelism < 1 {
		config.AttemptParallelism = 1
	}
	return config, nil
}

func decodeHex(value string, size int) ([]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != size {
		return nil, fmt.Errorf("expected %d bytes", size)
	}
	return decoded, nil
}
