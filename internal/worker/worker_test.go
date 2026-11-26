// These tests communicate through real Unix sockets and exercise faulty workers.
package worker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	pb "github.com/thomas/poml-node/api/proto"
	"github.com/thomas/poml-node/internal/codec"
	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/types"
	"github.com/thomas/poml-node/profiles"
	"google.golang.org/protobuf/proto"
)

func startWorker(t *testing.T, delay time.Duration) (Client, ed25519.PrivateKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "worker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (FixtureServer{Socket: socket, Certificate: key, InferenceDelay: delay}).Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if st, err := os.Stat(socket); err == nil && st.Mode().Perm() == 0600 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not create private socket")
		}
		time.Sleep(time.Millisecond)
	}
	return Client{Socket: socket}, key
}

func inferenceInput(t *testing.T, id string) InferenceRequest {
	t.Helper()
	s, _ := profiles.Fixture(id)
	tx := types.Tx{
		Kind: types.TxQuery, Sender: "user", QueryID: "q1", Input: []byte("private input"),
		ModelID: id, Recipient: bytes.Repeat([]byte{1}, 32), Expiry: 20,
	}
	tx.Commitment = types.HashBytes("input", tx.Input)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, 32))
	tx.Signature = cryptoutil.Sign(key, "transaction", codec.TxSigningBytes(tx))
	slots, err := cryptoutil.ExpandRandomness(s, []byte("example-attempt"), bytes.Repeat([]byte{9}, 64))
	if err != nil {
		t.Fatal(err)
	}
	return InferenceRequest{
		Spec: s, RequestID: "test-1", QueryID: tx.QueryID, AttemptID: "m-1", MinerID: "m",
		Commitment: tx.Commitment, SignedQuery: codec.TxBytes(tx), Input: tx.Input,
		Randomness: slots,
	}
}

func exampleWitness(req InferenceRequest, first InferenceResult) *pb.Witness {
	return &pb.Witness{
		Execution: first.Witness, Output: first.Output, EncryptionVrf: make([]byte, 64),
		EncryptionProof: make([]byte, 80), EncryptionInput: []byte("enc"),
		Recipient: make([]byte, 32), Randomness: req.Randomness, Input: req.Input,
		SignedQuery: req.SignedQuery, InferenceVrf: make([]byte, 64),
		InferenceProof: make([]byte, 80), InferencePublicKey: make([]byte, 32),
		EncryptionPublicKey: make([]byte, 32), AttemptInput: []byte("attempt"),
		ChainId: "test-chain",
	}
}

func TestWorkerProfilesAndCertificate(t *testing.T) {
	c, key := startWorker(t, 0)
	for _, id := range []string{"fixture-a", "fixture-b"} {
		t.Run(id, func(t *testing.T) {
			req := inferenceInput(t, id)
			first, err := c.Infer(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			second, err := c.Infer(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS == "linux" && first.CPUNanos == nil {
				t.Fatal("missing Linux thread-CPU sample")
			}
			if len(first.Output) != int(req.Spec.OutputBytes) || !bytes.Equal(first.Output, second.Output) || first.OperationCount == 0 {
				t.Fatal("fixture output or accounting mismatch")
			}
			wit := exampleWitness(req, first)
			msg := []byte("public statement")
			for _, purpose := range []string{"winner", "challenge"} {
				signature, _, err := c.Certify(t.Context(), req.Spec, "proof", purpose, msg, wit)
				if err != nil {
					t.Fatal(err)
				}
				if err := cryptoutil.Verify(key.Public().(ed25519.PublicKey), signature, "certificate", msg); err != nil {
					t.Fatal(err)
				}
				if cryptoutil.Verify(key.Public().(ed25519.PublicKey), signature, "certificate", []byte("changed")) == nil {
					t.Fatal("altered statement verified")
				}
			}
			if _, _, err := c.Certify(t.Context(), req.Spec, "proof", "winner", msg, nil); err == nil {
				t.Fatal("missing witness accepted")
			}
			req.Randomness[0] = nil
			if _, err := c.Infer(t.Context(), req); err == nil {
				t.Fatal("invalid public randomness accepted")
			}
			req = inferenceInput(t, id)
			req.Spec.TicketWeight++
			if _, err := c.Infer(t.Context(), req); err == nil {
				t.Fatal("changed worker profile accepted")
			}
		})
	}
}

func TestCanceledWorkerCall(t *testing.T) {
	c, _ := startWorker(t, 3*time.Second)
	req := inferenceInput(t, "fixture-a")
	ctx, cancel := context.WithCancel(t.Context())
	timer := time.AfterFunc(25*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	started := time.Now()
	_, err := c.Infer(ctx, req)
	if !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("cancellation: %v after %v", err, time.Since(started))
	}
}

func TestMismatchedWorkerReply(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "bad.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err = readFrame(conn); err != nil {
			return
		}
		payload, _ := proto.Marshal(&pb.WorkerResponse{Version: Version, RequestId: "another request", Ok: true})
		_ = writeFrame(conn, &pb.Envelope{Kind: pb.Envelope_WORKER_RESPONSE, Payload: payload})
	}()
	if _, err := (Client{Socket: socket}).Infer(t.Context(), inferenceInput(t, "fixture-a")); err == nil {
		t.Fatal("accepted a response for another request")
	}
}

// Inference and certification can be replaced independently by pointing at different services.
func TestSeparateProverSocket(t *testing.T) {
	inference, wrongKey := startWorker(t, 0)
	prover, key := startWorker(t, 0)
	c := Client{Socket: inference.Socket, CertificateSocket: prover.Socket}
	req := inferenceInput(t, "fixture-b")
	result, err := c.Infer(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	statement := []byte("statement")
	sig, _, err := c.Certify(t.Context(), req.Spec, "cert", "winner", statement, exampleWitness(req, result))
	if err != nil {
		t.Fatal(err)
	}
	if err := cryptoutil.Verify(key.Public().(ed25519.PublicKey), sig, "certificate", statement); err != nil {
		t.Fatal(err)
	}
	if cryptoutil.Verify(wrongKey.Public().(ed25519.PublicKey), sig, "certificate", statement) == nil {
		t.Fatal("inference service signed the winner")
	}
}
