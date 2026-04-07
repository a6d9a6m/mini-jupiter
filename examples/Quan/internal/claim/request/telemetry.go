package request

import (
	"context"
	"sync/atomic"
	"time"

	applog "mini-jupiter/pkg/log"

	"go.uber.org/zap"
)

type durationStat struct {
	count   atomic.Uint64
	totalNS atomic.Int64
	maxNS   atomic.Int64
}

func (s *durationStat) record(d time.Duration) {
	ns := d.Nanoseconds()
	if ns < 0 {
		ns = 0
	}
	s.count.Add(1)
	s.totalNS.Add(ns)
	for {
		current := s.maxNS.Load()
		if ns <= current {
			return
		}
		if s.maxNS.CompareAndSwap(current, ns) {
			return
		}
	}
}

func (s *durationStat) avgMS() float64 {
	count := s.count.Load()
	if count == 0 {
		return 0
	}
	return float64(s.totalNS.Load()) / float64(count) / 1e6
}

func (s *durationStat) maxMS() float64 {
	return float64(s.maxNS.Load()) / 1e6
}

type acceptTimingStats struct {
	calls    atomic.Uint64
	idem     durationStat
	decide   durationStat
	create   durationStat
	publish  durationStat
	enqueued durationStat
	total    durationStat
}

var globalAcceptTimingStats acceptTimingStats

func recordAcceptTiming(ctx context.Context, outcome string, idemLookup, decide, create, publish, markEnqueued, total time.Duration) {
	globalAcceptTimingStats.idem.record(idemLookup)
	globalAcceptTimingStats.decide.record(decide)
	globalAcceptTimingStats.create.record(create)
	globalAcceptTimingStats.publish.record(publish)
	globalAcceptTimingStats.enqueued.record(markEnqueued)
	globalAcceptTimingStats.total.record(total)

	calls := globalAcceptTimingStats.calls.Add(1)
	if calls%500 != 0 {
		return
	}

	applog.L(ctx).Info("claim accept timing summary",
		zap.Uint64("calls", calls),
		zap.String("last_outcome", outcome),
		zap.Float64("idem_lookup_avg_ms", globalAcceptTimingStats.idem.avgMS()),
		zap.Float64("idem_lookup_max_ms", globalAcceptTimingStats.idem.maxMS()),
		zap.Float64("decide_avg_ms", globalAcceptTimingStats.decide.avgMS()),
		zap.Float64("decide_max_ms", globalAcceptTimingStats.decide.maxMS()),
		zap.Float64("request_create_avg_ms", globalAcceptTimingStats.create.avgMS()),
		zap.Float64("request_create_max_ms", globalAcceptTimingStats.create.maxMS()),
		zap.Float64("publish_avg_ms", globalAcceptTimingStats.publish.avgMS()),
		zap.Float64("publish_max_ms", globalAcceptTimingStats.publish.maxMS()),
		zap.Float64("mark_enqueued_avg_ms", globalAcceptTimingStats.enqueued.avgMS()),
		zap.Float64("mark_enqueued_max_ms", globalAcceptTimingStats.enqueued.maxMS()),
		zap.Float64("total_avg_ms", globalAcceptTimingStats.total.avgMS()),
		zap.Float64("total_max_ms", globalAcceptTimingStats.total.maxMS()),
	)
}

type publishTimingStats struct {
	calls    atomic.Uint64
	acquire  durationStat
	topology durationStat
	publish  durationStat
	confirm  durationStat
	total    durationStat
}

var globalPublishTimingStats publishTimingStats

func recordPublishTiming(ctx context.Context, outcome string, acquireChannel, declareTopology, sendPublish, waitConfirm, total time.Duration) {
	globalPublishTimingStats.acquire.record(acquireChannel)
	globalPublishTimingStats.topology.record(declareTopology)
	globalPublishTimingStats.publish.record(sendPublish)
	globalPublishTimingStats.confirm.record(waitConfirm)
	globalPublishTimingStats.total.record(total)

	calls := globalPublishTimingStats.calls.Add(1)
	if calls%500 != 0 {
		return
	}

	applog.L(ctx).Info("claim publish timing summary",
		zap.Uint64("calls", calls),
		zap.String("last_outcome", outcome),
		zap.Float64("acquire_channel_avg_ms", globalPublishTimingStats.acquire.avgMS()),
		zap.Float64("acquire_channel_max_ms", globalPublishTimingStats.acquire.maxMS()),
		zap.Float64("declare_topology_avg_ms", globalPublishTimingStats.topology.avgMS()),
		zap.Float64("declare_topology_max_ms", globalPublishTimingStats.topology.maxMS()),
		zap.Float64("publish_avg_ms", globalPublishTimingStats.publish.avgMS()),
		zap.Float64("publish_max_ms", globalPublishTimingStats.publish.maxMS()),
		zap.Float64("confirm_avg_ms", globalPublishTimingStats.confirm.avgMS()),
		zap.Float64("confirm_max_ms", globalPublishTimingStats.confirm.maxMS()),
		zap.Float64("total_avg_ms", globalPublishTimingStats.total.avgMS()),
		zap.Float64("total_max_ms", globalPublishTimingStats.total.maxMS()),
	)
}
