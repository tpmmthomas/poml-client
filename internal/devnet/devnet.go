// Package devnet writes small independent-process configurations for local
// experiments. It is deliberately separate from consensus code.
package devnet

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/profile"
	"github.com/thomas/poml-node/profiles"
)

type UserFile struct {
	ID                  string `json:"id"`
	IdentityPrivateHex  string `json:"identity_private_key_hex"`
	RecipientPrivateHex string `json:"recipient_private_key_hex"`
	RecipientPublicHex  string `json:"recipient_public_key_hex"`
}

type Manifest struct {
	Genesis string   `json:"genesis"`
	Nodes   []string `json:"nodes"`
	Workers []string `json:"workers"`
	User    string   `json:"user"`
}

func Create(root string, count int) (Manifest, error) {
	if count < 1 || count > 16 {
		return Manifest{}, fmt.Errorf("node count must be between 1 and 16")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Manifest{}, err
	}
	certificatePublic, certificatePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Manifest{}, err
	}
	accounts := make([]profile.AccountFile, 0, count+1)
	miners := make([]profile.MinerFile, 0, count)
	nodeConfigs := make([]string, 0, count)
	workers := make([]string, 0, count)
	for index := 0; index < count; index++ {
		identityPublic, identityPrivate, keyErr := ed25519.GenerateKey(rand.Reader)
		if keyErr != nil {
			return Manifest{}, keyErr
		}
		inferenceSeed := randomBytes(32)
		encryptionSeed := randomBytes(32)
		_, _, inferencePublic, _ := cryptoutil.VRFProve(inferenceSeed, []byte("genesis-public-check"))
		_, _, encryptionPublic, _ := cryptoutil.VRFProve(encryptionSeed, []byte("genesis-public-check"))
		minerID := "miner-" + strconv.Itoa(index)
		miners = append(miners, profile.MinerFile{
			ID: minerID, IdentityPublicHex: hex.EncodeToString(identityPublic),
			InferenceVRFHex: hex.EncodeToString(inferencePublic), EncryptionVRFHex: hex.EncodeToString(encryptionPublic), Bond: 1000,
		})
		accounts = append(accounts, profile.AccountFile{ID: minerID, Balance: 1_000_000, PublicKeyHex: hex.EncodeToString(identityPublic)})
		workerSocket := filepath.Join(root, "run", "worker-"+strconv.Itoa(index)+".sock")
		workers = append(workers, workerSocket)
		configPath := filepath.Join(root, "node-"+strconv.Itoa(index)+".toml")
		seedLines := make([]string, 0, 1)
		if index > 0 {
			seedLines = append(seedLines, fmt.Sprintf("\"127.0.0.1:%d\"", 19000+index-1))
		}
		config := fmt.Sprintf(`node_id = "node-%d"
listen = "127.0.0.1:%d"
advertise = "127.0.0.1:%d"
seeds = [%s]
rpc = "127.0.0.1:%d"
rpc_token = "devnet-token"
data_dir = "run/node-%d"
genesis = "genesis.json"
worker_socket = "%s"
identity_private_key_hex = "%s"
inference_vrf_seed_hex = "%s"
encryption_vrf_seed_hex = "%s"
certificate_private_key_hex = "%s"
miner_id = "%s"
run_id = "devnet"
mining = true
max_attempts = 2000
attempt_parallelism = 2
`, index, 19000+index, 19000+index, strings.Join(seedLines, ", "), 19100+index, index,
			workerSocket, hex.EncodeToString(identityPrivate), hex.EncodeToString(inferenceSeed), hex.EncodeToString(encryptionSeed),
			hex.EncodeToString(certificatePrivate), minerID)
		if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
			return Manifest{}, err
		}
		nodeConfigs = append(nodeConfigs, configPath)
	}
	userPublic, userPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Manifest{}, err
	}
	recipientPrivate, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Manifest{}, err
	}
	user := UserFile{
		ID: "user-1", IdentityPrivateHex: hex.EncodeToString(userPrivate),
		RecipientPrivateHex: hex.EncodeToString(recipientPrivate.Bytes()), RecipientPublicHex: hex.EncodeToString(recipientPrivate.PublicKey().Bytes()),
	}
	accounts = append(accounts, profile.AccountFile{ID: user.ID, Balance: 1_000_000, PublicKeyHex: hex.EncodeToString(userPublic)})
	model, err := profiles.Fixture("fixture-a")
	if err != nil {
		return Manifest{}, err
	}
	genesis := profile.GenesisFile{
		Version: 1, ChainID: "poml-stage1-devnet", ProtocolVersion: 1, HashBits: 256,
		// 2^252 gives a roughly 1/16 single-attempt hit rate for quick local runs.
		BaseTargetHex: "1" + strings.Repeat("0", 63), ModelProfileID: "fixture-a", Model: model,
		CertificatePublicHex: hex.EncodeToString(certificatePublic), BlockReward: 1, QueryFee: 10,
		ChallengeWindow: 10, ResponseLimit: 128, MaxBlockBytes: 1 << 20,
		InitialAccounts: accounts, InitialMiners: miners,
	}
	genesisPath := filepath.Join(root, "genesis.json")
	if err := writeJSON(genesisPath, genesis); err != nil {
		return Manifest{}, err
	}
	if err := writeJSON(filepath.Join(root, "user.json"), user); err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{Genesis: genesisPath, Nodes: nodeConfigs, Workers: workers, User: filepath.Join(root, "user.json")}
	if err := writeJSON(filepath.Join(root, "manifest.json"), manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func randomBytes(size int) []byte {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return value
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
