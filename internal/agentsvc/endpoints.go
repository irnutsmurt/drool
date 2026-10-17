package agentsvc

import (
	"context"
	"database/sql"
	"errors"

	"drool/internal/agentrpc/pb"
	"drool/internal/ids"
	"drool/internal/store/db"
)

// --- endpoint migration (DESIGN §6.5) ---

// ProposeEndpoint starts a migration: every connected agent stores and
// tests the candidate; nothing changes on an agent until it confirmed it.
func (s *Service) ProposeEndpoint(ctx context.Context, endpoint, userID string) (string, error) {
	id := ids.New(ids.AgentEndpoint)
	if err := s.st.W.InsertEndpointMigration(ctx, db.InsertEndpointMigrationParams{ID: id, Endpoint: endpoint,
		CreatedBy: sql.NullString{String: userID, Valid: userID != ""}, CreatedAt: s.now().UnixMilli()}); err != nil {
		return "", err
	}
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, se := range s.sessions {
		sessions = append(sessions, se)
	}
	s.mu.Unlock()
	for _, se := range sessions {
		se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_EndpointCandidate{EndpointCandidate: &pb.EndpointCandidate{Id: id, Endpoint: endpoint}}})
	}
	return id, nil
}

func (s *Service) onEndpointTest(ctx context.Context, se *Session, r *pb.EndpointTestResult) {
	s.st.W.UpsertEndpointTest(ctx, db.UpsertEndpointTestParams{MigrationID: r.Id, IdentityID: se.ident.ID, Ok: boolInt(r.Ok), Error: r.Error, TestedAt: s.now().UnixMilli()})
	m, err := s.st.R.GetEndpointMigration(ctx, r.Id)
	if err == nil && m.Status == "committed" && r.Ok {
		s.commitFor(ctx, se, m)
	}
}

func (s *Service) commitFor(ctx context.Context, se *Session, m db.AgentEndpointMigration) {
	se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_EndpointCommit{EndpointCommit: &pb.EndpointCommit{Id: m.ID, Endpoint: m.Endpoint}}})
	s.st.W.MarkEndpointTestCommitted(ctx, db.MarkEndpointTestCommittedParams{MigrationID: m.ID, IdentityID: se.ident.ID})
}

// CommitEndpoint makes the candidate primary on every agent that confirmed
// it (now, or when it confirms later). Agents that couldn't reach it keep
// their current endpoint: the change can't strand them.
func (s *Service) CommitEndpoint(ctx context.Context, id string) (int, error) {
	m, err := s.st.R.GetEndpointMigration(ctx, id)
	if err != nil {
		return 0, err
	}
	if m.Status == "cancelled" {
		return 0, errors.New("this migration was cancelled")
	}
	if err := s.st.W.SetEndpointMigrationStatus(ctx, db.SetEndpointMigrationStatusParams{Status: "committed",
		CommittedAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}, ID: id}); err != nil {
		return 0, err
	}
	m.Status = "committed"
	tests, _ := s.st.R.ListEndpointTests(ctx, id)
	n := 0
	for _, t := range tests {
		if t.Ok == 0 {
			continue
		}
		for _, se := range s.liveSessions(t.IdentityID) {
			s.commitFor(ctx, se, m)
			n++
		}
	}
	return n, nil
}
