package agentsvc

import (
	"drool/internal/agentrpc/pb"
)

// MonitorHooks connect agent sessions to system monitoring (M11).
type MonitorHooks interface {
	// OnSession runs after a session's hello was acknowledged.
	OnSession(hostID string, features []string, effectiveJSON []byte)
	// OnTelemetry receives the monitoring kinds (metrics, monitor_state,
	// services, storage, events, diagnostics).
	OnTelemetry(hostID, kind string, payload []byte)
}

// MonitorKinds are the telemetry kinds routed to MonitorHooks.
var MonitorKinds = map[string]bool{"metrics": true, "monitor_state": true, "services": true, "storage": true,
	"events": true, "diagnostics": true}

func (s *Service) monitorHooks() MonitorHooks {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hooks.Monitor
}

// SetMonitorHooks installs the monitoring service's hooks.
func (s *Service) SetMonitorHooks(h MonitorHooks) {
	s.mu.Lock()
	s.hooks.Monitor = h
	s.mu.Unlock()
}

// SendMonitorConfig pushes a monitoring config to a connected agent.
func (s *Service) SendMonitorConfig(hostID string, configJSON []byte) bool {
	se, ok := s.SessionFor(hostID)
	if !ok || se == nil {
		return false
	}
	return se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_MonitorConfig{MonitorConfig: &pb.MonitorConfig{ConfigJson: configJSON}}}) == nil
}

// RequestTelemetry asks a connected agent for one telemetry kind now.
func (s *Service) RequestTelemetry(hostID, kind string) bool {
	se, ok := s.SessionFor(hostID)
	if !ok || se == nil {
		return false
	}
	return se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_RequestTelemetry{RequestTelemetry: &pb.RequestTelemetry{Kind: kind}}}) == nil
}

// SessionFeatures returns the connected agent's announced features.
func (s *Service) SessionFeatures(hostID string) ([]string, bool) {
	se, ok := s.SessionFor(hostID)
	if !ok || se == nil {
		return nil, false
	}
	return se.hello.Features, true
}
