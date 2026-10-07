package sshdriver

import (
	"context"
	"fmt"
	"time"

	"drool/internal/driver"
	"drool/internal/hostops"
	"drool/internal/model"
	"drool/internal/sshx"
)

// Reboot polling (ARCHITECTURE-ALPHA §10.4).
var (
	rebootPollStart = 3 * time.Second
	rebootPollMax   = 15 * time.Second
)

// waitReboot polls with fresh connections until the host answers with the
// pinned host key and a new boot id.
func (x *Executor) waitReboot(ctx context.Context, t *sshx.Target, res *hostops.RebootResult, out driver.OutputSink) error {
	before := res.BootIDBefore
	start := time.Now()
	var wentAway, lastOld time.Time // first failed poll; last answer with the old boot id
	delay := rebootPollStart
	lastErr := ""
	for {
		select {
		case <-ctx.Done():
			// Judge by the latest contact: a slow handshake in between is not a reboot.
			if wentAway.IsZero() || lastOld.After(wentAway) {
				res.NotRebooted = true
				return &model.OpError{Class: model.ClassTimeout,
					Message: "the host never went away; it still reports the old boot id (the reboot did not happen)",
					Hint:    "The reboot was requested but the host kept running. Something may be blocking shutdown (a hung service or an inhibitor lock); check the host, or reboot it from its console."}
			}
			return &model.OpError{Class: model.ClassTimeout,
				Message: fmt.Sprintf("the host went down but did not come back within the timeout (last error: %s)", lastErr),
				Hint:    "Check the host's console or power state. Its status on the host page updates by itself once it answers again."}
		case <-time.After(delay):
		}
		delay = min(delay*2, rebootPollMax)
		fresh, err := x.Pool.Dialer.Dial(ctx, t)
		if err != nil {
			if model.ClassOf(err) == model.ClassHostKey {
				return err // never trust a different key after a reboot
			}
			if wentAway.IsZero() {
				wentAway = time.Now()
				hostops.Note(out, "host went away after %s", time.Since(start).Round(time.Second))
			}
			lastErr = err.Error()
			continue
		}
		id, err := hostops.ReadBootID(ctx, sshRunner{fresh})
		fresh.Close()
		if err != nil {
			lastErr = err.Error()
			continue
		}
		if id == before {
			lastOld = time.Now()
			continue // not rebooted yet
		}
		driver.SetPhase(out, "verifying")
		res.BootIDAfter = id
		res.TotalSeconds = time.Since(start).Seconds()
		if !wentAway.IsZero() {
			res.DownSeconds = time.Since(wentAway).Seconds()
		}
		hostops.Note(out, "host is back after %s with boot id %s", time.Since(start).Round(time.Second), id)
		return nil
	}
}
