// pomlproof validates a private auxiliary witness and signs its public
// statement. It is a tiny local bridge for Python model workers. Consensus
// never trusts the helper's result directly because peers verify the returned
// statement signature and proof bundle themselves.
package main

import (
	"crypto/ed25519"
	"flag"
	"fmt"
	"io"
	"os"

	pb "github.com/thomas/poml-node/api/proto"
	cryptoutil "github.com/thomas/poml-node/internal/crypto"
	"github.com/thomas/poml-node/internal/worker"
	"google.golang.org/protobuf/proto"
)

func main() {
	keyHex := flag.String("certificate-private-key-hex", "", "Ed25519 key used for the local certificate")
	flag.Parse()
	keyBytes, err := cryptoutil.DecodeHex(*keyHex, ed25519.PrivateKeySize)
	if err != nil {
		fail(err)
	}
	requestBytes, err := io.ReadAll(io.LimitReader(os.Stdin, 16<<20))
	if err != nil {
		fail(err)
	}
	var request pb.WorkerRequest
	if err := proto.Unmarshal(requestBytes, &request); err != nil {
		fail(fmt.Errorf("decode worker request: %w", err))
	}
	if request.Kind != pb.WorkerRequest_CERTIFICATE || request.Purpose == "" || len(request.Statement) == 0 {
		fail(fmt.Errorf("request is not a certificate job"))
	}
	if err := worker.ValidateAuxiliaryWitness(&request); err != nil {
		fail(err)
	}
	if _, err := os.Stdout.Write(cryptoutil.Sign(ed25519.PrivateKey(keyBytes), "certificate", request.Statement)); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
