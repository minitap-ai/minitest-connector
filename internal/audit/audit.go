// Package audit emits security audit events. Every event is a structured log
// line whose "event" starts with "private_network."; the cluster log pipeline
// (Alloy) labels them log_type=audit, which puts them in the 366-day Loki audit
// class and the GCS archive.
//
// Audit attributes are identifiers, hostnames, ports, byte counts, durations
// and outcomes only. Never pass tokens, headers or payload bytes.
package audit

import (
	"context"
	"log/slog"
)

// Event names.
const (
	ConnectorAuthFailed   = "private_network.connector_auth_failed"
	InstanceConnected     = "private_network.instance_connected"
	InstanceDisconnected  = "private_network.instance_disconnected"
	InstanceRejected      = "private_network.instance_rejected"
	StreamOpened          = "private_network.stream_opened"
	StreamClosed          = "private_network.stream_closed"
	StreamDenied          = "private_network.stream_denied"
	ConnectorSessionsDrop = "private_network.connector_sessions_dropped"
)

// Logger writes audit events.
type Logger struct{ l *slog.Logger }

// New returns an audit logger writing through l.
func New(l *slog.Logger) *Logger { return &Logger{l: l} }

// Event writes one audit event.
func (a *Logger) Event(ctx context.Context, event string, attrs ...any) {
	a.l.InfoContext(ctx, event, append([]any{"event", event, "audit", true}, attrs...)...)
}
