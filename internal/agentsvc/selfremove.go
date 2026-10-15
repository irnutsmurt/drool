package agentsvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"

	"drool/internal/agentrpc/pb"
)

// FeatureUninstall: the Shepherd removes itself when asked (dogfooding
// correction batch).
const FeatureUninstall = "uninstall-1"

// Why a Shepherd can't be asked to remove itself.
var (
	ErrNotConnected = errors.New("the Shepherd isn't connected (or more than one machine presents its identity)")
	ErrTooOld       = errors.New("this Shepherd can't remove itself: it's from before Shepherds could")
)

// SessionFeature reports whether the host's connected Shepherd announced a
// feature in this session.
func (s *Service) SessionFeature(hostID, feature string) (connected, has bool) {
	se, single := s.SessionFor(hostID)
	if se == nil || !single {
		return false, false
	}
	return true, slices.Contains(se.hello.GetFeatures(), feature)
}

// RequestSelfUninstall asks the host's connected Shepherd to remove itself
// and waits (at most wait) for its answer: accepted means its removal
// helper runs, detached from it. The Shepherd answers a repeated request
// with the removal it already started. The caller holds the host's lock.
func (s *Service) RequestSelfUninstall(ctx context.Context, hostID, reason string, wait time.Duration) (*pb.SelfUninstallResult, error) {
	se, single := s.SessionFor(hostID)
	if se == nil || !single {
		return nil, ErrNotConnected
	}
	if !slices.Contains(se.hello.GetFeatures(), FeatureUninstall) {
		return nil, ErrTooOld
	}
	b := make([]byte, 12)
	rand.Read(b)
	now := s.now()
	req := &pb.SelfUninstall{RequestId: "rm_" + hex.EncodeToString(b), IdentityId: se.ident.ID, ControllerEpoch: s.Epoch(),
		IssuedAtUnix: now.Unix(), ExpiresAtUnix: now.Add(2 * time.Minute).Unix(), Reason: reason}
	ch := make(chan *pb.SelfUninstallResult, 1)
	se.mu.Lock()
	if se.removeWait == nil {
		se.removeWait = map[string]chan *pb.SelfUninstallResult{}
	}
	se.removeWait[req.RequestId] = ch
	se.mu.Unlock()
	defer func() {
		se.mu.Lock()
		delete(se.removeWait, req.RequestId)
		se.mu.Unlock()
	}()
	if err := se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_SelfUninstall{SelfUninstall: req}}); err != nil {
		return nil, fmt.Errorf("couldn't send the request: %w", err)
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case r := <-ch:
		return r, nil
	case <-se.done:
		// The answer may have been on its way when the connection ended.
		select {
		case r := <-ch:
			return r, nil
		default:
		}
		return nil, errors.New("the Shepherd disconnected before answering")
	case <-t.C:
		return nil, fmt.Errorf("the Shepherd didn't answer within %s", wait)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (se *Session) onSelfUninstallResult(r *pb.SelfUninstallResult) {
	se.mu.Lock()
	ch := se.removeWait[r.RequestId]
	se.mu.Unlock()
	if ch != nil {
		select {
		case ch <- r:
		default:
		}
	}
}
