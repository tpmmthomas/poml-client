// Package worker is the replaceable, versioned inference and certification process boundary.
package worker

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"time"

	pb "github.com/thomas/poml-node/api/proto"
	"github.com/thomas/poml-node/internal/types"
	"github.com/thomas/poml-node/profiles"
	"google.golang.org/protobuf/proto"
)

// Version applies to both requests and responses on the private worker socket.
const Version = 1

// Client opens a separate bounded socket call for each attempt. Closing a call
// interrupts reads immediately when its mining parent becomes stale.
type Client struct {
	Socket string
	// CertificateSocket selects a separate prover when supplied.
	CertificateSocket string
	Timeout           time.Duration
}

// InferenceRequest carries everything an inference backend can use for this attempt.
type InferenceRequest struct {
	Spec                                   profiles.Spec
	RequestID, QueryID, AttemptID, MinerID string
	Parent, Prefix, Commitment             types.Hash
	SignedQuery, Input                     []byte
	Randomness                             [][]byte
}

// InferenceResult holds private bytes until the miner seals or queues this result.
type InferenceResult struct {
	Output, Witness []byte
	OperationCount  uint64
	Timing
}

// Timing records fixture wall time and the worker-thread CPU sample when available.
type Timing struct {
	Duration time.Duration
	CPUNanos *uint64
}

// CertificateResult separates the statement binding signature from an
// optional model proof produced by a real backend.
type CertificateResult struct {
	Certificate []byte
	ModelProof  []byte
	Timing
}

// Infer checks the profile, actual output length, digest and opaque witness returned by a worker.
func (c Client) Infer(ctx context.Context, r InferenceRequest) (InferenceResult, error) {
	model, err := types.ParseHash(r.Spec.ModelDigest)
	if err != nil {
		return InferenceResult{}, err
	}
	request := baseRequest(r.RequestID, r.Spec)
	request.Kind = pb.WorkerRequest_INFERENCE
	request.QueryId, request.AttemptId, request.MinerId = r.QueryID, r.AttemptID, r.MinerID
	request.ModelDigest, request.Parent, request.PrefixDigest = model[:], r.Parent[:], r.Prefix[:]
	request.Input, request.InputCommitment, request.SignedQuery = r.Input, r.Commitment[:], r.SignedQuery
	request.Randomness = r.Randomness
	reply, err := c.call(ctx, request)
	if err != nil {
		return InferenceResult{}, err
	}
	digest := types.HashBytes("worker-output", reply.Output)
	if len(reply.Output) == 0 || len(reply.Output) > int(r.Spec.OutputBytes) || len(reply.ExecutionWitness) == 0 ||
		!validShape(reply.OutputShape, len(reply.Output)) || !bytes.Equal(reply.ResultDigest, digest[:]) {
		return InferenceResult{}, errors.New("invalid worker output, shape, digest or witness")
	}
	return InferenceResult{
		Output: reply.Output, Witness: reply.ExecutionWitness,
		OperationCount: reply.OperationCount,
		Timing:         Timing{Duration: time.Duration(reply.DurationNs), CPUNanos: reply.CpuNs},
	}, nil
}

// Certify supplies public bytes and private witness to a separately replaceable
// prover. The caller independently verifies the result with the genesis public key.
func (c Client) Certify(ctx context.Context, spec profiles.Spec, id, purpose string, statement []byte, wit *pb.Witness) ([]byte, Timing, error) {
	result, err := c.CertifyWithProof(ctx, spec, id, purpose, statement, wit)
	return result.Certificate, result.Timing, err
}

// CertifyWithProof is the extended worker call used by Stage 2 proof
// profiles. Legacy callers can keep using Certify and receive only the
// statement binding certificate.
func (c Client) CertifyWithProof(ctx context.Context, spec profiles.Spec, id, purpose string, statement []byte, wit *pb.Witness) (CertificateResult, error) {
	request := baseRequest(id, spec)
	request.Kind = pb.WorkerRequest_CERTIFICATE
	request.Purpose, request.Statement, request.Witness = purpose, statement, wit
	reply, err := c.call(ctx, request)
	if err != nil {
		return CertificateResult{}, err
	}
	digest := types.HashBytes("worker-certificate", reply.Certificate)
	if len(reply.Certificate) == 0 || !bytes.Equal(reply.ResultDigest, digest[:]) {
		return CertificateResult{}, errors.New("worker certificate digest mismatch")
	}
	return CertificateResult{Certificate: reply.Certificate, ModelProof: reply.ModelProof,
		Timing: Timing{Duration: time.Duration(reply.DurationNs), CPUNanos: reply.CpuNs}}, nil
}

func baseRequest(id string, spec profiles.Spec) *pb.WorkerRequest {
	digest := spec.Digest()
	return &pb.WorkerRequest{
		RequestId: id, Version: Version, ProfileId: spec.ID, ProfileDigest: digest[:],
		CertificateProfile: spec.CertificateProfile, OutputLimit: spec.OutputBytes,
		RuntimeMs: spec.MaxRuntimeMS,
	}
}

func (c Client) call(ctx context.Context, request *pb.WorkerRequest) (*pb.WorkerResponse, error) {
	timeout := time.Duration(request.RuntimeMs) * time.Millisecond
	if c.Timeout > 0 && c.Timeout < timeout {
		timeout = c.Timeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	socket := c.Socket
	if request.Kind == pb.WorkerRequest_CERTIFICATE && c.CertificateSocket != "" {
		socket = c.CertificateSocket
	}
	connection, err := (&net.Dialer{}).DialContext(callCtx, "unix", socket)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	stop := context.AfterFunc(callCtx, func() { connection.Close() })
	defer stop()
	payload, err := proto.Marshal(request)
	if err != nil {
		return nil, err
	}
	if err := writeFrame(connection, &pb.Envelope{Kind: pb.Envelope_WORKER_REQUEST, Payload: payload}); err != nil {
		return nil, err
	}
	frame, err := readFrame(connection)
	if callCtx.Err() != nil {
		return nil, callCtx.Err()
	}
	if err != nil {
		return nil, err
	}
	if frame.Kind != pb.Envelope_WORKER_RESPONSE {
		return nil, errors.New("unexpected worker message kind")
	}
	var response pb.WorkerResponse
	if err := proto.Unmarshal(frame.Payload, &response); err != nil {
		return nil, err
	}
	if response.Version != Version || response.RequestId != request.RequestId || !bytes.Equal(response.ProfileDigest, request.ProfileDigest) {
		return nil, errors.New("worker request ID, version or profile mismatch")
	}
	if response.DurationNs > uint64(time.Duration(request.RuntimeMs)*time.Millisecond) {
		return nil, errors.New("reported worker time exceeds the request budget")
	}
	if !response.Ok {
		message := strings.TrimSpace(response.Error)
		if message == "" {
			message = "worker rejected the request"
		}
		return nil, errors.New(message)
	}
	return &response, nil
}

// Output dimensions are byte dimensions. Bound the product before multiplication.
func validShape(shape []uint32, size int) bool {
	if len(shape) == 0 || len(shape) > 16 {
		return false
	}
	product := uint64(1)
	for _, d := range shape {
		if d == 0 || product > uint64(size)/uint64(d) {
			return false
		}
		product *= uint64(d)
	}
	return product == uint64(size)
}
