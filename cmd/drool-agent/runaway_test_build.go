//go:build droolrunaway

package main

import (
	"context"
	"os"
	"runtime"
	"strings"
	"time"
)

// runawayCollector is a deliberately broken monitoring collector for the
// resource-safety test (only built with -tags droolrunaway): while
// /etc/drool/runaway-test says "cpu" it spins on every CPU; while it says
// "mem" it allocates without bound. The agent's systemd budget must
// contain it.
var runawayCollector = func(ctx context.Context) {
	var hoard [][]byte
	stop := make(chan struct{})
	spinning := false
	for {
		b, _ := os.ReadFile("/etc/drool/runaway-test")
		mode := strings.TrimSpace(string(b))
		switch {
		case mode == "cpu" && !spinning:
			spinning = true
			stop = make(chan struct{})
			for i := 0; i < runtime.NumCPU()*2; i++ {
				go func(stop chan struct{}) {
					x := 0
					for {
						select {
						case <-stop:
							return
						default:
							for j := 0; j < 1_000_000; j++ {
								x += j
							}
						}
					}
				}(stop)
			}
		case mode != "cpu" && spinning:
			close(stop)
			spinning = false
		}
		if mode == "mem" {
			chunk := make([]byte, 16<<20)
			for i := range chunk {
				chunk[i] = byte(i)
			}
			hoard = append(hoard, chunk)
		} else {
			hoard = nil
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}
