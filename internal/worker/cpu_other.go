//go:build !linux

// Leave the CPU sample absent on hosts without the Linux thread-accounting interface.
package worker

import pb "github.com/thomas/poml-node/api/proto"

func measureCPU(_ *pb.WorkerResponse, run func() error) error { return run() }
