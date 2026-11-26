// pomlctl submits signed transactions and inspects a running node.
package main

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/thomas/poml-node/internal/codec"
	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/devnet"
	"github.com/thomas/poml-node/internal/rpc"
	"github.com/thomas/poml-node/internal/types"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init-devnet":
		initDevnet(os.Args[2:])
	case "submit-query":
		submitQuery(os.Args[2:])
	case "get-tip", "node-info", "start-mining", "stop-mining":
		callSimple(os.Args[1], os.Args[2:])
	case "get-block":
		getBlock(os.Args[2:])
	case "decrypt":
		decryptBlock(os.Args[2:])
	case "keygen":
		keygen()
	default:
		usage()
		os.Exit(2)
	}
}

func initDevnet(args []string) {
	flags := flag.NewFlagSet("init-devnet", flag.ExitOnError)
	root := flags.String("dir", "run/devnet", "output directory")
	nodes := flags.Int("nodes", 3, "number of node processes")
	model := flags.String("model", "fixture-a", "model profile: fixture-a, dummy, diffusion-small or gemma3-1b")
	flags.Parse(args)
	manifest, err := devnet.Create(*root, *nodes, *model)
	if err != nil {
		fatal(err)
	}
	printJSON(manifest)
}

func submitQuery(args []string) {
	flags := flag.NewFlagSet("submit-query", flag.ExitOnError)
	node := flags.String("node", "127.0.0.1:19100", "node RPC address")
	token := flags.String("token", "devnet-token", "RPC bearer token")
	userPath := flags.String("user", "run/devnet/user.json", "user key file")
	sender := flags.String("sender", "user-1", "sender account ID")
	nonce := flags.Uint64("nonce", 0, "sender account nonce")
	input := flags.String("input", "hello", "input text")
	model := flags.String("model", "fixture-a", "model profile ID")
	expiry := flags.Uint64("expiry", 100, "query expiry height")
	fee := flags.Uint64("fee", 10, "service fee")
	queryID := flags.String("query-id", "", "optional query ID")
	flags.Parse(args)
	data, err := os.ReadFile(*userPath)
	if err != nil {
		fatal(err)
	}
	var user devnet.UserFile
	if err := json.Unmarshal(data, &user); err != nil {
		fatal(err)
	}
	privateBytes, err := cryptoutil.DecodeHex(user.IdentityPrivateHex, ed25519.PrivateKeySize)
	if err != nil {
		fatal(err)
	}
	identity := ed25519.PrivateKey(privateBytes)
	recipient, err := cryptoutil.DecodeHex(user.RecipientPublicHex, 32)
	if err != nil {
		fatal(err)
	}
	inputBytes := []byte(*input)
	if *queryID == "" {
		id := types.HashBytes("query", []byte(*sender), u64Bytes(*nonce), inputBytes)
		*queryID = id.String()
	}
	tx := types.Tx{
		Kind: types.TxQuery, Sender: *sender, Nonce: *nonce, QueryID: *queryID,
		Input: inputBytes, Commitment: types.HashBytes("input", inputBytes), Recipient: recipient,
		ModelID: *model, Expiry: *expiry, Fee: *fee,
	}
	tx.Signature = cryptoutil.Sign(identity, "transaction", codec.TxSigningBytes(tx))
	result, err := rpc.Call(context.Background(), *node, *token, "submit_query", map[string]string{
		"transaction_base64": base64.StdEncoding.EncodeToString(codec.TxBytes(tx)),
	})
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(result))
}

func callSimple(method string, args []string) {
	flags := flag.NewFlagSet(method, flag.ExitOnError)
	node := flags.String("node", "127.0.0.1:19100", "node RPC address")
	token := flags.String("token", "devnet-token", "RPC bearer token")
	flags.Parse(args)
	result, err := rpc.Call(context.Background(), *node, *token, methodName(method), map[string]any{})
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(result))
}

func getBlock(args []string) {
	flags := flag.NewFlagSet("get-block", flag.ExitOnError)
	node := flags.String("node", "127.0.0.1:19100", "node RPC address")
	token := flags.String("token", "devnet-token", "RPC bearer token")
	hash := flags.String("hash", "", "block hash")
	flags.Parse(args)
	result, err := rpc.Call(context.Background(), *node, *token, "get_block", map[string]string{"hash": *hash})
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(result))
}

func decryptBlock(args []string) {
	flags := flag.NewFlagSet("decrypt", flag.ExitOnError)
	node := flags.String("node", "127.0.0.1:19100", "node RPC address")
	token := flags.String("token", "devnet-token", "RPC bearer token")
	userPath := flags.String("user", "run/devnet/user.json", "user key file")
	hash := flags.String("hash", "", "block hash containing the winner")
	flags.Parse(args)
	result, err := rpc.Call(context.Background(), *node, *token, "get_block", map[string]string{"hash": *hash})
	if err != nil {
		fatal(err)
	}
	var blockResult struct {
		BlockBase64 string `json:"block_base64"`
	}
	if err := json.Unmarshal(result, &blockResult); err != nil {
		fatal(err)
	}
	blockBytes, err := base64.StdEncoding.DecodeString(blockResult.BlockBase64)
	if err != nil {
		fatal(err)
	}
	block, err := codec.DecodeBlock(blockBytes)
	if err != nil {
		fatal(err)
	}
	userBytes, err := os.ReadFile(*userPath)
	if err != nil {
		fatal(err)
	}
	var user devnet.UserFile
	if err := json.Unmarshal(userBytes, &user); err != nil {
		fatal(err)
	}
	privateKey, err := cryptoutil.DecodeHex(user.RecipientPrivateHex, 32)
	if err != nil {
		fatal(err)
	}
	nodeInfoBytes, err := rpc.Call(context.Background(), *node, *token, "get_node_info", map[string]any{})
	if err != nil {
		fatal(err)
	}
	var nodeInfo struct {
		ChainID string `json:"chain_id"`
	}
	if err := json.Unmarshal(nodeInfoBytes, &nodeInfo); err != nil || nodeInfo.ChainID == "" {
		fatal(errors.New("node did not return a chain ID"))
	}
	plaintext, err := cryptoutil.OpenOutput(privateKey, nodeInfo.ChainID, block.Winner)
	if err != nil {
		fatal(err)
	}
	fmt.Println(hex.EncodeToString(plaintext))
}

func methodName(command string) string {
	return map[string]string{"get-tip": "get_tip", "node-info": "get_node_info", "start-mining": "start_mining", "stop-mining": "stop_mining"}[command]
}

func keygen() {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fatal(err)
	}
	recipient, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		fatal(err)
	}
	printJSON(map[string]string{
		"identity_public_key_hex":   hex.EncodeToString(public),
		"identity_private_key_hex":  hex.EncodeToString(private),
		"recipient_public_key_hex":  hex.EncodeToString(recipient.PublicKey().Bytes()),
		"recipient_private_key_hex": hex.EncodeToString(recipient.Bytes()),
	})
}

func printJSON(value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(data))
}

func u64Bytes(value uint64) []byte {
	return []byte(fmt.Sprintf("%020d", value))
}

func usage() {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(`pomlctl commands:
  init-devnet --dir run/devnet --nodes 3
  submit-query --node 127.0.0.1:19100 --user run/devnet/user.json --input hello
  get-tip --node 127.0.0.1:19100
  get-block --node 127.0.0.1:19100 --hash <hash>
  decrypt --node 127.0.0.1:19100 --user run/devnet/user.json --hash <hash>
  node-info --node 127.0.0.1:19100
  keygen`))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
