package relay

import (
	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	sessions      prometheus.Gauge
	activeStreams prometheus.Gauge
	streams       *prometheus.CounterVec
	bytes         *prometheus.CounterVec
	authFailures  prometheus.Counter
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		sessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "relay_connector_sessions", Help: "Connector sessions held by this relay pod.",
		}),
		activeStreams: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "relay_active_streams", Help: "Device streams currently open through this relay pod.",
		}),
		streams: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relay_streams_total", Help: "Device stream attempts by result.",
		}, []string{"result"}),
		bytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "relay_stream_bytes_total", Help: "Bytes relayed by direction (up = device to customer).",
		}, []string{"direction"}),
		authFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "relay_connector_auth_failures_total", Help: "Rejected connector tokens.",
		}),
	}
	if reg != nil {
		reg.MustRegister(m.sessions, m.activeStreams, m.streams, m.bytes, m.authFailures)
	}
	return m
}
