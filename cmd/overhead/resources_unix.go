//go:build linux || darwin

package main

import (
	"runtime"
	"syscall"
)

type resourceUsage struct {
	CPU float64
	RSS int64
}

func resources() resourceUsage {
	var r syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &r); err != nil {
		panic(err)
	}
	rss := r.Maxrss
	if runtime.GOOS == "darwin" {
		rss /= 1024
	}
	return resourceUsage{float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6, rss}
}
