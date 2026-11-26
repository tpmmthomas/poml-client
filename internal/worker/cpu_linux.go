//go:build linux

// Count CPU for this worker call only. Concurrent requests use other threads.
package worker

import (
	"runtime"
	"syscall"

	pb "github.com/thomas/poml-node/api/proto"
)

func measureCPU(reply *pb.WorkerResponse, run func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var start, end syscall.Rusage
	first := syscall.Getrusage(syscall.RUSAGE_THREAD, &start)
	err := run()
	last := syscall.Getrusage(syscall.RUSAGE_THREAD, &end)
	if first == nil && last == nil {
		ns := uint64(end.Stime.Nano() + end.Utime.Nano() - start.Stime.Nano() - start.Utime.Nano())
		reply.CpuNs = &ns
	}
	return err
}
