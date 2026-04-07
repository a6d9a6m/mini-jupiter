package request

import (
	"context"
	"errors"
	"time"

	apperr "mini-jupiter/pkg/errors"
)

type AcceptService struct {
	hotpath   HotPath
	store     RequestStore
	publisher Publisher
	metrics   acceptMetrics
}

func NewAcceptService(hotpath HotPath, store RequestStore, publisher Publisher) *AcceptService {
	return &AcceptService{
		hotpath:   hotpath,
		store:     store,
		publisher: publisher,
		metrics:   noopAcceptMetrics{},
	}
}

type acceptMetrics interface {
	IncClaimRequestState(status string)
}

type noopAcceptMetrics struct{}

func (noopAcceptMetrics) IncClaimRequestState(string) {}

const durabilityPendingWarning = "accepted_without_replica_confirmation"

func acceptDurabilityWarning(err error) (string, error) {
	if err == nil {
		return "", nil
	}
	if _, ok := AsDurabilityPendingError(err); ok {
		return durabilityPendingWarning, nil
	}
	return "", err
}

func ignoreDurabilityPending(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := AsDurabilityPendingError(err); ok {
		return nil
	}
	return err
}

func classifyStatusUpdate(err error) (applied bool, warning string, normalized error) {
	if err == nil {
		return true, "", nil
	}
	if _, ok := AsDurabilityPendingError(err); ok {
		return true, durabilityPendingWarning, nil
	}
	if _, ok := AsTransitionSkippedError(err); ok {
		return false, "", nil
	}
	return false, "", err
}

func (s *AcceptService) SetMetrics(metrics acceptMetrics) {
	if s == nil || metrics == nil {
		return
	}
	s.metrics = metrics
}

func (s *AcceptService) Accept(ctx context.Context, req AcceptRequest) (AcceptResponse, error) {
	startedAt := time.Now()
	var (
		idemLookupDur  time.Duration
		decideDur      time.Duration
		createDur      time.Duration
		publishDur     time.Duration
		markEnqueueDur time.Duration
	)

	stageStart := time.Now()
	if existing, found, err := s.store.FindByIdempotency(ctx, req.CouponID, req.UserID, req.IdempotencyKey); err != nil {
		idemLookupDur = time.Since(stageStart)
		recordAcceptTiming(ctx, "idem_error", idemLookupDur, decideDur, createDur, publishDur, markEnqueueDur, time.Since(startedAt))
		return AcceptResponse{}, err
	} else if found {
		idemLookupDur = time.Since(stageStart)
		recordAcceptTiming(ctx, "idem_hit", idemLookupDur, decideDur, createDur, publishDur, markEnqueueDur, time.Since(startedAt))
		return AcceptResponse{RequestID: existing.ID, Status: existing.Status}, nil
	}
	idemLookupDur = time.Since(stageStart)

	stageStart = time.Now()
	decision, err := s.hotpath.Decide(ctx, req.CouponID, req.UserID, req.IdempotencyKey)
	decideDur = time.Since(stageStart)
	if err != nil {
		recordAcceptTiming(ctx, "decide_error", idemLookupDur, decideDur, createDur, publishDur, markEnqueueDur, time.Since(startedAt))
		return AcceptResponse{}, err
	}
	if decision.Code == DecisionCodeIdemHit {
		recordAcceptTiming(ctx, "idem_hit_from_hotpath", idemLookupDur, decideDur, createDur, publishDur, markEnqueueDur, time.Since(startedAt))
		return AcceptResponse{RequestID: decision.RequestID, Status: StatusProcessing}, nil
	}
	if decision.Code != DecisionCodeAdmitted {
		recordAcceptTiming(ctx, "rejected", idemLookupDur, decideDur, createDur, publishDur, markEnqueueDur, time.Since(startedAt))
		return AcceptResponse{}, errors.New("request rejected")
	}

	record := Request{
		ID:             decision.RequestID,
		CouponID:       req.CouponID,
		UserID:         req.UserID,
		IdempotencyKey: req.IdempotencyKey,
		ReservationID:  decision.RequestID,
		Status:         StatusAccepted,
	}
	stageStart = time.Now()
	respWarning := ""
	createErr := s.store.Create(ctx, record)
	if createErr != nil {
		createDur = time.Since(stageStart)
		if warning, normalizedErr := acceptDurabilityWarning(createErr); normalizedErr == nil {
			if warning != "" {
				respWarning = warning
			}
			createErr = nil
		}
	}
	if createErr != nil {
		if errors.Is(createErr, ErrRequestExists) {
			existing, found, findErr := s.store.FindByIdempotency(ctx, req.CouponID, req.UserID, req.IdempotencyKey)
			if findErr != nil {
				recordAcceptTiming(ctx, "create_exists_find_error", idemLookupDur, decideDur, createDur, publishDur, markEnqueueDur, time.Since(startedAt))
				return AcceptResponse{}, findErr
			}
			if found {
				recordAcceptTiming(ctx, "create_exists", idemLookupDur, decideDur, createDur, publishDur, markEnqueueDur, time.Since(startedAt))
				return AcceptResponse{RequestID: existing.ID, Status: existing.Status}, nil
			}
		}
		recordAcceptTiming(ctx, "create_error", idemLookupDur, decideDur, createDur, publishDur, markEnqueueDur, time.Since(startedAt))
		return AcceptResponse{}, createErr
	}
	createDur = time.Since(stageStart)
	s.metrics.IncClaimRequestState(string(StatusAccepted))

	stageStart = time.Now()
	if err := s.publisher.PublishAccepted(ctx, record); err != nil {
		publishDur = time.Since(stageStart)
		stageStart = time.Now()
		updateErr := s.store.UpdateStatus(ctx, record.ID, StatusPublishing, 0, "")
		if applied, warning, normalizedErr := classifyStatusUpdate(updateErr); normalizedErr == nil {
			if warning != "" {
				respWarning = warning
			}
			updateErr = nil
			if applied {
				s.metrics.IncClaimRequestState(string(StatusPublishing))
			}
		}
		if updateErr != nil {
			markEnqueueDur = time.Since(stageStart)
			recordAcceptTiming(ctx, "publish_error_mark_error", idemLookupDur, decideDur, createDur, publishDur, markEnqueueDur, time.Since(startedAt))
			return AcceptResponse{}, updateErr
		}
		markEnqueueDur = time.Since(stageStart)
		recordAcceptTiming(ctx, "publish_error", idemLookupDur, decideDur, createDur, publishDur, markEnqueueDur, time.Since(startedAt))
		return AcceptResponse{RequestID: record.ID, Status: StatusPublishing, Warning: respWarning}, nil
	}
	publishDur = time.Since(stageStart)
	stageStart = time.Now()
	updateErr := s.store.UpdateStatus(ctx, record.ID, StatusEnqueued, 0, "")
	applied := false
	if a, warning, normalizedErr := classifyStatusUpdate(updateErr); normalizedErr == nil {
		applied = a
		if warning != "" {
			respWarning = warning
		}
		updateErr = nil
	}
	if updateErr != nil {
		markEnqueueDur = time.Since(stageStart)
		recordAcceptTiming(ctx, "mark_enqueued_error", idemLookupDur, decideDur, createDur, publishDur, markEnqueueDur, time.Since(startedAt))
		return AcceptResponse{}, updateErr
	}
	markEnqueueDur = time.Since(stageStart)
	if applied {
		s.metrics.IncClaimRequestState(string(StatusEnqueued))
	}
	recordAcceptTiming(ctx, "accepted", idemLookupDur, decideDur, createDur, publishDur, markEnqueueDur, time.Since(startedAt))
	return AcceptResponse{RequestID: record.ID, Status: StatusEnqueued, Warning: respWarning}, nil
}

type Consumer struct {
	store   RequestStore
	writer  ClaimWriter
	hotpath HotPath
	metrics consumeMetrics
}

func NewConsumer(store RequestStore, writer ClaimWriter, hotpath HotPath) *Consumer {
	return &Consumer{
		store:   store,
		writer:  writer,
		hotpath: hotpath,
		metrics: noopConsumeMetrics{},
	}
}

type consumeMetrics interface {
	ObserveClaimRequestConsume(result string, duration time.Duration)
	IncClaimRequestState(status string)
}

type noopConsumeMetrics struct{}

func (noopConsumeMetrics) ObserveClaimRequestConsume(string, time.Duration) {}
func (noopConsumeMetrics) IncClaimRequestState(string)                      {}

func (c *Consumer) SetMetrics(metrics consumeMetrics) {
	if c == nil || metrics == nil {
		return
	}
	c.metrics = metrics
}

func (c *Consumer) ConsumeAccepted(ctx context.Context, requestID string) error {
	startedAt := time.Now()
	result := "unknown"
	defer func() {
		c.metrics.ObserveClaimRequestConsume(result, time.Since(startedAt))
	}()

	req, found, err := c.store.Get(ctx, requestID)
	if err != nil {
		result = "load_error"
		return err
	}
	if !found {
		result = "request_not_found"
		return ErrRequestNotFound
	}

	if req.Status == StatusSucceeded || req.Status == StatusRolledBack || req.Status == StatusFailed {
		result = "terminal_noop"
		return nil
	}
	applied, _, err := classifyStatusUpdate(c.store.UpdateStatus(ctx, requestID, StatusProcessing, 0, ""))
	if err != nil {
		result = "mark_processing_error"
		return err
	}
	if !applied {
		result = "processing_transition_skipped"
		return nil
	}
	c.metrics.IncClaimRequestState(string(StatusProcessing))

	claimID, _, err := c.writer.PersistClaim(ctx, req)
	if err != nil {
		if IsRetryableError(err) {
			result = "retryable_persist_error"
			return err
		}
		if rollbackErr := c.hotpath.Rollback(ctx, req.CouponID, req.UserID, req.IdempotencyKey, requestReservationID(req)); rollbackErr != nil {
			result = "rollback_error"
			return rollbackErr
		}
		applied, _, updateErr := classifyStatusUpdate(c.store.UpdateStatus(ctx, requestID, StatusRolledBack, 0, err.Error()))
		if updateErr != nil {
			result = "mark_rolled_back_error"
			return updateErr
		}
		if applied {
			c.metrics.IncClaimRequestState(string(StatusRolledBack))
		}
		result = "rolled_back"
		return nil
	}
	if err := c.hotpath.Finalize(ctx, req.CouponID, req.UserID, req.IdempotencyKey, requestReservationID(req), claimID); err != nil {
		result = "finalize_error"
		return err
	}
	applied, _, err = classifyStatusUpdate(c.store.UpdateStatus(ctx, requestID, StatusSucceeded, claimID, ""))
	if err != nil {
		result = "mark_succeeded_error"
		return err
	}
	if applied {
		c.metrics.IncClaimRequestState(string(StatusSucceeded))
	}
	result = "succeeded"
	return nil
}

type QueryService struct {
	store RequestStore
}

func NewQueryService(store RequestStore) *QueryService {
	return &QueryService{store: store}
}

func (s *QueryService) Get(ctx context.Context, requestID string) (QueryResult, error) {
	req, found, err := s.store.Get(ctx, requestID)
	if err != nil {
		return QueryResult{}, err
	}
	if !found {
		return QueryResult{}, apperr.New(apperr.CodeNotFound, "request not found")
	}

	result := QueryResult{
		RequestID:   req.ID,
		Internal:    req.Status,
		ClaimID:     req.ClaimID,
		FailureCode: req.FailureCode,
	}
	switch req.Status {
	case StatusSucceeded:
		result.State = ResultStateSucceeded
	case StatusRolledBack, StatusFailed:
		result.State = ResultStateFailed
	default:
		result.State = ResultStateProcessing
	}
	return result, nil
}

type AppService struct {
	accept *AcceptService
	query  *QueryService
}

func NewAppService(accept *AcceptService, query *QueryService) *AppService {
	return &AppService{accept: accept, query: query}
}

func (s *AppService) Accept(ctx context.Context, req AcceptRequest) (AcceptResponse, error) {
	return s.accept.Accept(ctx, req)
}

func (s *AppService) Get(ctx context.Context, requestID string) (QueryResult, error) {
	return s.query.Get(ctx, requestID)
}

type Reconciler struct {
	store     RequestStore
	publisher Publisher
	hotpath   HotPath
	claims    ClaimLookup
	policy    ReconcilePolicy
	metrics   reconcileMetrics
}

func NewReconciler(store RequestStore, publisher Publisher, hotpath HotPath, claims ClaimLookup, policy ReconcilePolicy) *Reconciler {
	return &Reconciler{
		store:     store,
		publisher: publisher,
		hotpath:   hotpath,
		claims:    claims,
		policy:    policy.withDefaults(),
		metrics:   noopReconcileMetrics{},
	}
}

type reconcileMetrics interface {
	ObserveClaimRequestReconcile(action, result string, duration time.Duration)
	IncClaimRequestState(status string)
}

type noopReconcileMetrics struct{}

func (noopReconcileMetrics) ObserveClaimRequestReconcile(string, string, time.Duration) {}
func (noopReconcileMetrics) IncClaimRequestState(string)                                {}

func (r *Reconciler) SetMetrics(metrics reconcileMetrics) {
	if r == nil || metrics == nil {
		return
	}
	r.metrics = metrics
}

func (r *Reconciler) ReconcileOnce(ctx context.Context, limit int) error {
	now := time.Now().UTC()
	requests, err := r.store.ListByStatuses(ctx, []Status{
		StatusAccepted,
		StatusPublishing,
		StatusEnqueued,
		StatusProcessing,
	}, limit)
	if err != nil {
		return err
	}
	for _, req := range requests {
		actionStartedAt := time.Now()
		action := ""
		result := ""
		switch req.Status {
		case StatusAccepted, StatusPublishing:
			action = "accepted_republish"
			if now.Sub(req.UpdatedAt) < r.policy.PublishStaleAfter {
				result = "skip_fresh"
				continue
			}
			applied, updateErr := r.store.CompareAndUpdateStatus(ctx, req, StatusEnqueued, 0, "")
			_, _, err = classifyStatusUpdate(updateErr)
			if err != nil {
				result = "mark_enqueued_error"
				r.metrics.ObserveClaimRequestReconcile(action, result, time.Since(actionStartedAt))
				return err
			}
			if !applied {
				result = "skip_stale"
				continue
			}
			if err := r.publisher.PublishAccepted(ctx, req); err != nil {
				result = "publish_error"
				r.metrics.ObserveClaimRequestReconcile(action, result, time.Since(actionStartedAt))
				continue
			}
			if applied {
				r.metrics.IncClaimRequestState(string(StatusEnqueued))
			}
			result = "succeeded"
		case StatusEnqueued:
			action = "enqueued_republish"
			if now.Sub(req.UpdatedAt) < r.policy.ProcessingStaleAfter {
				result = "skip_fresh"
				continue
			}
			applied, updateErr := r.store.CompareAndUpdateStatus(ctx, req, StatusEnqueued, 0, "")
			_, _, err = classifyStatusUpdate(updateErr)
			if err != nil {
				result = "mark_enqueued_error"
				r.metrics.ObserveClaimRequestReconcile(action, result, time.Since(actionStartedAt))
				return err
			}
			if !applied {
				result = "skip_stale"
				continue
			}
			if err := r.publisher.PublishAccepted(ctx, req); err != nil {
				result = "publish_error"
				r.metrics.ObserveClaimRequestReconcile(action, result, time.Since(actionStartedAt))
				continue
			}
			if applied {
				r.metrics.IncClaimRequestState(string(StatusEnqueued))
			}
			result = "succeeded"
		case StatusProcessing:
			if now.Sub(req.UpdatedAt) < r.policy.ProcessingStaleAfter {
				action = "processing_repair"
				result = "skip_fresh"
				continue
			}
			claimID, found, err := r.claims.FindClaimID(ctx, req)
			if err != nil {
				r.metrics.ObserveClaimRequestReconcile("processing_lookup", "lookup_error", time.Since(actionStartedAt))
				return err
			}
			if found {
				action = "processing_finalize"
				if err := r.hotpath.Finalize(ctx, req.CouponID, req.UserID, req.IdempotencyKey, requestReservationID(req), claimID); err != nil {
					result = "finalize_error"
					r.metrics.ObserveClaimRequestReconcile(action, result, time.Since(actionStartedAt))
					return err
				}
				applied, _, err := classifyStatusUpdate(r.store.UpdateStatus(ctx, req.ID, StatusSucceeded, claimID, ""))
				if err != nil {
					result = "mark_succeeded_error"
					r.metrics.ObserveClaimRequestReconcile(action, result, time.Since(actionStartedAt))
					return err
				}
				if applied {
					r.metrics.IncClaimRequestState(string(StatusSucceeded))
				}
				result = "succeeded"
				continue
			}
			action = "processing_republish"
			applied, updateErr := r.store.CompareAndUpdateStatus(ctx, req, StatusEnqueued, 0, "")
			_, _, err = classifyStatusUpdate(updateErr)
			if err != nil {
				result = "mark_enqueued_error"
				r.metrics.ObserveClaimRequestReconcile(action, result, time.Since(actionStartedAt))
				return err
			}
			if !applied {
				result = "skip_stale"
				continue
			}
			if err := r.publisher.PublishAccepted(ctx, req); err != nil {
				result = "publish_error"
				r.metrics.ObserveClaimRequestReconcile(action, result, time.Since(actionStartedAt))
				continue
			}
			if applied {
				r.metrics.IncClaimRequestState(string(StatusEnqueued))
			}
			result = "succeeded"
		}
		if action != "" {
			r.metrics.ObserveClaimRequestReconcile(action, result, time.Since(actionStartedAt))
		}
	}
	return nil
}

func requestReservationID(req Request) string {
	if req.ReservationID != "" {
		return req.ReservationID
	}
	return req.ID
}
