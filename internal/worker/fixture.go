// This private socket server is a deterministic model and certificate fixture.
package worker

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha512"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	pb "github.com/thomas/poml-node/api/proto"
	"github.com/thomas/poml-node/internal/codec"
	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/types"
	"github.com/thomas/poml-node/profiles"
	"google.golang.org/protobuf/proto"
)

// FixtureServer accepts the two shipped synthetic profiles behind one private socket.
// Delay knobs model scheduling only and never alter output bytes or ticket weight.
type FixtureServer struct {
	Socket                     string
	Certificate                ed25519.PrivateKey
	InferenceDelay, ProofDelay time.Duration
}

// Serve removes only a stale socket and restricts new connections to the socket owner.
func (s FixtureServer) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.Socket), 0700); err != nil {
		return err
	}
	if st, err := os.Lstat(s.Socket); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return errors.New("worker socket path already contains a file")
		}
		existing, dialErr := net.DialTimeout("unix", s.Socket, 100*time.Millisecond)
		if dialErr == nil {
			existing.Close()
			return errors.New("worker socket is already listening")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return dialErr
		}
		if err := os.Remove(s.Socket); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", s.Socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(s.Socket, 0600); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	var handlers sync.WaitGroup
	defer handlers.Wait()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		handlers.Go(func() { s.handle(ctx, conn) })
	}
}

func (s FixtureServer) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(time.Hour)); err != nil {
		return
	}
	frame, err := readFrame(conn)
	if err != nil || frame.Kind != pb.Envelope_WORKER_REQUEST {
		return
	}
	var request pb.WorkerRequest
	if proto.Unmarshal(frame.Payload, &request) != nil {
		return
	}
	// A caller that has closed its connection has withdrawn the work.
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { var b [1]byte; _, _ = conn.Read(b[:]); cancel() }()
	started := time.Now()
	reply := &pb.WorkerResponse{Version: Version, RequestId: request.RequestId, ProfileDigest: request.ProfileDigest}
	if err := s.run(callCtx, &request, reply); err != nil {
		reply.Error = err.Error()
	} else {
		reply.Ok = true
	}
	reply.DurationNs = uint64(time.Since(started))
	payload, err := proto.Marshal(reply)
	if err == nil {
		_ = writeFrame(conn, &pb.Envelope{Kind: pb.Envelope_WORKER_RESPONSE, Payload: payload})
	}
}

func (s FixtureServer) run(ctx context.Context, r *pb.WorkerRequest, reply *pb.WorkerResponse) error {
	spec, err := profiles.Fixture(r.ProfileId)
	if err != nil {
		return err
	}
	want := spec.Digest()
	if r.Version != Version || r.RequestId == "" || !bytes.Equal(r.ProfileDigest, want[:]) ||
		r.CertificateProfile != spec.CertificateProfile || r.OutputLimit != spec.OutputBytes || r.RuntimeMs != spec.MaxRuntimeMS {
		return errors.New("fixture profile or worker version mismatch")
	}
	switch r.Kind {
	case pb.WorkerRequest_INFERENCE:
		if err := delay(ctx, s.InferenceDelay); err != nil {
			return err
		}
		return measureCPU(reply, func() error { return fixtureInference(spec, r, reply) })
	case pb.WorkerRequest_CERTIFICATE:
		if err := delay(ctx, s.ProofDelay); err != nil {
			return err
		}
		return measureCPU(reply, func() error { return s.fixtureCertificate(r, reply) })
	default:
		return errors.New("unknown worker request kind")
	}
}

func (s FixtureServer) fixtureCertificate(r *pb.WorkerRequest, reply *pb.WorkerResponse) error {
	if (r.Purpose != "winner" && r.Purpose != "challenge") || len(r.Statement) == 0 || len(s.Certificate) != ed25519.PrivateKeySize {
		return errors.New("invalid certificate request")
	}
	w := r.Witness
	if w == nil || len(w.Execution) == 0 || len(w.Output) == 0 || len(w.Input) == 0 || len(w.SignedQuery) == 0 {
		return errors.New("missing private execution or query witness")
	}
	if len(w.EncryptionVrf) != 64 || len(w.EncryptionProof) != 80 || len(w.EncryptionPublicKey) != 32 ||
		len(w.EncryptionInput) == 0 || len(w.Recipient) != 32 || w.ChainId == "" {
		return errors.New("incomplete encryption witness")
	}
	if len(w.InferenceVrf) != 64 || len(w.InferenceProof) != 80 || len(w.InferencePublicKey) != 32 ||
		len(w.Randomness) == 0 || len(w.AttemptInput) == 0 {
		return errors.New("incomplete inference randomness witness")
	}
	reply.Certificate = cryptoutil.Sign(s.Certificate, "certificate", r.Statement)
	digest := types.HashBytes("worker-certificate", reply.Certificate)
	reply.ResultDigest = digest[:]
	return nil
}

func delay(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func fixtureInference(spec profiles.Spec, r *pb.WorkerRequest, reply *pb.WorkerResponse) error {
	model, _ := types.ParseHash(spec.ModelDigest)
	commit := types.HashBytes("input", r.Input)
	if len(r.Input) == 0 || !bytes.Equal(commit[:], r.InputCommitment) || !bytes.Equal(model[:], r.ModelDigest) {
		return errors.New("worker input or model digest mismatch")
	}
	if len(r.Parent) != 32 || len(r.PrefixDigest) != 32 || r.QueryId == "" || r.MinerId == "" ||
		r.AttemptId == "" || len(r.Randomness) != int(spec.RandomnessSlots) {
		return errors.New("incomplete attempt metadata or randomness")
	}
	query, err := codec.DecodeTx(r.SignedQuery)
	if err != nil || query.Kind != types.TxQuery || query.QueryID != r.QueryId ||
		query.ModelID != spec.ID || query.Commitment != commit || len(query.Signature) != ed25519.SignatureSize ||
		(len(query.Input) > 0 && !bytes.Equal(query.Input, r.Input)) {
		return errors.New("query metadata does not match verified input")
	}
	fields := [][]byte{r.ModelDigest, r.Input}
	for _, slot := range r.Randomness {
		if len(slot) != int(spec.RandomnessBytes) {
			return errors.New("incorrect randomness slot length")
		}
		fields = append(fields, slot)
	}
	output := types.HashBytes("fixture-inference", fields...)
	reply.Output = output[:]
	if spec.ID == "fixture-b" {
		wide := sha512.Sum512(output[:])
		reply.Output = wide[:]
	}
	digest := types.HashBytes("worker-output", reply.Output)
	witness := types.HashBytes("fixture-witness", digest[:], r.Parent, r.PrefixDigest, []byte(r.AttemptId))
	reply.ExecutionWitness, reply.ResultDigest = witness[:], digest[:]
	reply.OutputShape = []uint32{uint32(len(reply.Output))}
	reply.OperationCount = uint64(len(r.Input)) + uint64(spec.RandomnessBytes)*uint64(spec.RandomnessSlots) + 1
	return nil
}
