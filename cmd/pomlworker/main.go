// pomlworker serves the deterministic Stage 1 inference and certificate fixture.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/profile"
	"github.com/thomas/poml-node/internal/worker"
)

func main() {
	configPath := flag.String("config", "", "node TOML containing the worker socket and fixture key")
	socket := flag.String("socket", "", "override the Unix socket path")
	keyPath := flag.String("key-file", "", "file containing the hexadecimal Ed25519 fixture key")
	legacyKey := flag.String("certificate-private-key-hex", "", "local test key (prefer --config or --key-file)")
	inferDelay := flag.Duration("inference-delay", 0, "simulated inference latency, e.g. 50ms")
	proofDelay := flag.Duration("proof-delay", 0, "simulated winner/challenge proof latency")
	flag.Parse()
	keyHex := *legacyKey
	if *configPath != "" {
		cfg, err := profile.LoadNodeConfig(*configPath)
		if err != nil {
			fail(err)
		}
		if *socket == "" {
			*socket = cfg.WorkerSocket
			if !filepath.IsAbs(*socket) {
				*socket = filepath.Join(filepath.Dir(*configPath), *socket)
			}
		}
		keyHex = cfg.CertificatePrivHex
	}
	if *keyPath != "" {
		data, err := os.ReadFile(*keyPath)
		if err != nil {
			fail(err)
		}
		keyHex = string(bytes.TrimSpace(data))
	}
	if *socket == "" {
		fail(fmt.Errorf("provide --config or --socket"))
	}
	key, err := cryptoutil.DecodeHex(keyHex, ed25519.PrivateKeySize)
	if err != nil {
		fail(err)
	}
	if *inferDelay < 0 || *proofDelay < 0 {
		fail(fmt.Errorf("fixture delays must be nonnegative"))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	s := worker.FixtureServer{Socket: *socket, Certificate: key, InferenceDelay: *inferDelay, ProofDelay: *proofDelay}
	if err := s.Serve(ctx); err != nil {
		fail(err)
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
