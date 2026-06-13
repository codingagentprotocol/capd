package server

import (
	"sync"
	"sync/atomic"
)

type runtimeMetrics struct {
	adapterStarts        atomic.Int64
	adapterStartFailures atomic.Int64
	routeFailures        atomic.Int64
	secretAccessDenied   atomic.Int64

	mu             sync.Mutex
	routeDecisions map[string]int64
}

func newRuntimeMetrics() *runtimeMetrics {
	return &runtimeMetrics{routeDecisions: make(map[string]int64)}
}

func (m *runtimeMetrics) recordAdapterStart(ok bool) {
	if m == nil {
		return
	}
	m.adapterStarts.Add(1)
	if !ok {
		m.adapterStartFailures.Add(1)
	}
}

func (m *runtimeMetrics) recordRouteDecision(agentID string, ok bool) {
	if m == nil {
		return
	}
	if !ok {
		m.routeFailures.Add(1)
		return
	}
	if agentID == "" {
		return
	}
	m.mu.Lock()
	m.routeDecisions[agentID]++
	m.mu.Unlock()
}

func (m *runtimeMetrics) recordSecretAccessDenied() {
	if m == nil {
		return
	}
	m.secretAccessDenied.Add(1)
}

func (m *runtimeMetrics) snapshot() map[string]any {
	if m == nil {
		return map[string]any{
			"adapterStarts":        int64(0),
			"adapterStartFailures": int64(0),
			"routeFailures":        int64(0),
			"secretAccessDenied":   int64(0),
			"routeDecisions":       map[string]int64{},
		}
	}
	m.mu.Lock()
	routeDecisions := make(map[string]int64, len(m.routeDecisions))
	for agentID, count := range m.routeDecisions {
		routeDecisions[agentID] = count
	}
	m.mu.Unlock()
	return map[string]any{
		"adapterStarts":        m.adapterStarts.Load(),
		"adapterStartFailures": m.adapterStartFailures.Load(),
		"routeFailures":        m.routeFailures.Load(),
		"secretAccessDenied":   m.secretAccessDenied.Load(),
		"routeDecisions":       routeDecisions,
	}
}
