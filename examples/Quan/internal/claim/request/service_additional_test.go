package request

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcceptService_IdempotencyHitReturnsExistingRequestWithoutDecisionOrPublish(t *testing.T) {
	store := newFakeRequestStore()
	existing := Request{
		ID:             "req-idem-hit",
		CouponID:       1011,
		UserID:         2011,
		IdempotencyKey: "idem-hit",
		Status:         StatusSucceeded,
		ClaimID:        9001,
	}
	if err := store.Create(context.Background(), existing); err != nil {
		t.Fatalf("seed request failed: %v", err)
	}
	hotpath := &countingHotPath{fakeHotPath: fakeHotPath{
		decision: Decision{Code: DecisionCodeAdmitted, RequestID: "req-should-not-happen"},
	}}
	pub := &fakePublisher{}
	svc := NewAcceptService(hotpath, store, pub)

	got, err := svc.Accept(context.Background(), AcceptRequest{
		CouponID:       existing.CouponID,
		UserID:         existing.UserID,
		IdempotencyKey: existing.IdempotencyKey,
	})
	if err != nil {
		t.Fatalf("accept should reuse existing request: %v", err)
	}
	if got.RequestID != existing.ID || got.Status != existing.Status {
		t.Fatalf("expected existing request %+v, got %+v", existing, got)
	}
	if hotpath.decideCalls != 0 {
		t.Fatalf("expected no decide call on idempotency hit, got %d", hotpath.decideCalls)
	}
	if len(pub.published) != 0 {
		t.Fatalf("expected no publish on idempotency hit, got %+v", pub.published)
	}
}

func TestAcceptService_CreateExistsReturnsExistingRequest(t *testing.T) {
	existing := Request{
		ID:             "req-existing",
		CouponID:       1012,
		UserID:         2012,
		IdempotencyKey: "idem-existing",
		Status:         StatusPublishing,
	}
	store := &createExistsStore{existing: existing}
	hotpath := &countingHotPath{fakeHotPath: fakeHotPath{
		decision: Decision{Code: DecisionCodeAdmitted, RequestID: "req-new"},
	}}
	pub := &fakePublisher{}
	svc := NewAcceptService(hotpath, store, pub)

	got, err := svc.Accept(context.Background(), AcceptRequest{
		CouponID:       existing.CouponID,
		UserID:         existing.UserID,
		IdempotencyKey: existing.IdempotencyKey,
	})
	if err != nil {
		t.Fatalf("accept should reuse existing request on create conflict: %v", err)
	}
	if got.RequestID != existing.ID || got.Status != existing.Status {
		t.Fatalf("expected existing request %+v, got %+v", existing, got)
	}
	if hotpath.decideCalls != 1 {
		t.Fatalf("expected a single decide call before create conflict, got %d", hotpath.decideCalls)
	}
	if store.findCalls != 2 {
		t.Fatalf("expected two idempotency lookups, got %d", store.findCalls)
	}
	if len(pub.published) != 0 {
		t.Fatalf("expected no publish when create reports existing request, got %+v", pub.published)
	}
}

func TestAcceptService_HotPathIdemHitReturnsRequestHandle(t *testing.T) {
	store := newFakeRequestStore()
	hotpath := &fakeHotPath{
		decision: Decision{Code: DecisionCodeIdemHit, RequestID: "req-idem-hotpath"},
	}
	pub := &fakePublisher{}
	svc := NewAcceptService(hotpath, store, pub)

	got, err := svc.Accept(context.Background(), AcceptRequest{
		CouponID:       1012,
		UserID:         2012,
		IdempotencyKey: "idem-hotpath",
	})
	if err != nil {
		t.Fatalf("accept should return request handle for hotpath idem hit: %v", err)
	}
	if got.RequestID != "req-idem-hotpath" || got.Status != StatusProcessing {
		t.Fatalf("expected processing request handle, got %+v", got)
	}
	if len(pub.published) != 0 {
		t.Fatalf("expected no publish on hotpath idem hit, got %+v", pub.published)
	}
}

func TestAcceptService_CreateDurabilityPendingReturnsWarningAndHandle(t *testing.T) {
	store := &createDurabilityPendingStore{
		RequestStore: newFakeRequestStore(),
	}
	pub := &fakePublisher{}
	hotpath := &fakeHotPath{
		decision: Decision{Code: DecisionCodeAdmitted, RequestID: "req-create-pending"},
	}
	svc := NewAcceptService(hotpath, store, pub)

	got, err := svc.Accept(context.Background(), AcceptRequest{
		CouponID:       1013,
		UserID:         2013,
		IdempotencyKey: "idem-create-pending",
	})
	if err != nil {
		t.Fatalf("accept should downgrade create durability pending: %v", err)
	}
	if got.RequestID != "req-create-pending" || got.Status != StatusEnqueued {
		t.Fatalf("expected accepted handle after create durability pending, got %+v", got)
	}
	if got.Warning != durabilityPendingWarning {
		t.Fatalf("expected durability warning, got %+v", got)
	}
}

func TestAcceptService_MarkEnqueuedDurabilityPendingReturnsWarningAndHandle(t *testing.T) {
	store := &statusUpdateAfterSuccessErrorStore{
		RequestStore: newFakeRequestStore(),
		errorsByStatus: map[Status]error{
			StatusEnqueued: DurabilityPendingError{
				RequestID: "req-enqueued-pending",
				Status:    StatusEnqueued,
				Err:       errors.New("replica confirmation incomplete"),
			},
		},
	}
	pub := &fakePublisher{}
	hotpath := &fakeHotPath{
		decision: Decision{Code: DecisionCodeAdmitted, RequestID: "req-enqueued-pending"},
	}
	svc := NewAcceptService(hotpath, store, pub)

	got, err := svc.Accept(context.Background(), AcceptRequest{
		CouponID:       1014,
		UserID:         2014,
		IdempotencyKey: "idem-enqueued-pending",
	})
	if err != nil {
		t.Fatalf("accept should downgrade mark enqueued durability pending: %v", err)
	}
	if got.RequestID != "req-enqueued-pending" || got.Status != StatusEnqueued {
		t.Fatalf("expected enqueued handle after durability pending, got %+v", got)
	}
	if got.Warning != durabilityPendingWarning {
		t.Fatalf("expected durability warning, got %+v", got)
	}
	req, found, getErr := store.Get(context.Background(), "req-enqueued-pending")
	if getErr != nil {
		t.Fatalf("load request failed: %v", getErr)
	}
	if !found || req.Status != StatusEnqueued {
		t.Fatalf("expected request to be updated before warning return, got found=%v req=%+v", found, req)
	}
}

func TestAcceptService_PublishFailureMarkStatusFailureReturnsUpdateError(t *testing.T) {
	store := &statusUpdateErrorStore{
		RequestStore: newFakeRequestStore(),
		errorsByStatus: map[Status]error{
			StatusPublishing: errors.New("mark publishing failed"),
		},
	}
	pub := &fakePublisher{err: errors.New("mq unavailable")}
	hotpath := &fakeHotPath{
		decision: Decision{Code: DecisionCodeAdmitted, RequestID: "req-mark-error"},
	}
	svc := NewAcceptService(hotpath, store, pub)

	_, err := svc.Accept(context.Background(), AcceptRequest{
		CouponID:       1013,
		UserID:         2013,
		IdempotencyKey: "idem-mark-error",
	})
	if err == nil || err.Error() != "mark publishing failed" {
		t.Fatalf("expected mark publishing failure, got %v", err)
	}

	req, found, getErr := store.Get(context.Background(), "req-mark-error")
	if getErr != nil {
		t.Fatalf("load request failed: %v", getErr)
	}
	if !found || req.Status != StatusAccepted {
		t.Fatalf("expected request to remain accepted after mark failure, got found=%v req=%+v", found, req)
	}
}

func TestConsumer_RequestNotFoundReturnsDomainError(t *testing.T) {
	consumer := NewConsumer(newFakeRequestStore(), &countingClaimWriter{}, &fakeHotPath{})

	err := consumer.ConsumeAccepted(context.Background(), "req-missing")
	if !errors.Is(err, ErrRequestNotFound) {
		t.Fatalf("expected ErrRequestNotFound, got %v", err)
	}
}

func TestConsumer_TerminalRequestNoopsWithoutPersist(t *testing.T) {
	store := newFakeRequestStore()
	if err := store.Create(context.Background(), Request{
		ID:             "req-terminal",
		CouponID:       1014,
		UserID:         2014,
		IdempotencyKey: "idem-terminal",
		Status:         StatusSucceeded,
		ClaimID:        9014,
	}); err != nil {
		t.Fatalf("seed request failed: %v", err)
	}
	writer := &countingClaimWriter{claimID: 1234, inserted: true}
	hotpath := &countingHotPath{}
	consumer := NewConsumer(store, writer, hotpath)

	if err := consumer.ConsumeAccepted(context.Background(), "req-terminal"); err != nil {
		t.Fatalf("terminal request should noop: %v", err)
	}
	if writer.calls != 0 {
		t.Fatalf("expected no persist attempt for terminal request, got %d", writer.calls)
	}
	if hotpath.finalizeCalls != 0 || hotpath.rollbackCalls != 0 {
		t.Fatalf("expected no finalize/rollback for terminal request, got finalize=%d rollback=%d", hotpath.finalizeCalls, hotpath.rollbackCalls)
	}
}

func TestConsumer_MarkProcessingFailureStopsBeforePersist(t *testing.T) {
	store := &statusUpdateErrorStore{
		RequestStore: newFakeRequestStore(),
		errorsByStatus: map[Status]error{
			StatusProcessing: errors.New("mark processing failed"),
		},
	}
	if err := store.Create(context.Background(), Request{
		ID:             "req-processing-mark-error",
		CouponID:       1015,
		UserID:         2015,
		IdempotencyKey: "idem-processing-mark-error",
		Status:         StatusEnqueued,
	}); err != nil {
		t.Fatalf("seed request failed: %v", err)
	}
	writer := &countingClaimWriter{claimID: 9015, inserted: true}
	consumer := NewConsumer(store, writer, &fakeHotPath{})

	err := consumer.ConsumeAccepted(context.Background(), "req-processing-mark-error")
	if err == nil || err.Error() != "mark processing failed" {
		t.Fatalf("expected mark processing failure, got %v", err)
	}
	if writer.calls != 0 {
		t.Fatalf("expected no persist attempt after mark processing error, got %d", writer.calls)
	}
}

func TestConsumer_DurabilityPendingStatusUpdateStillConverges(t *testing.T) {
	store := &statusUpdateAfterSuccessErrorStore{
		RequestStore: newFakeRequestStore(),
		errorsByStatus: map[Status]error{
			StatusProcessing: DurabilityPendingError{
				RequestID: "req-processing-pending",
				Status:    StatusProcessing,
				Err:       errors.New("replica confirmation incomplete"),
			},
			StatusSucceeded: DurabilityPendingError{
				RequestID: "req-processing-pending",
				Status:    StatusSucceeded,
				Err:       errors.New("replica confirmation incomplete"),
			},
		},
	}
	if err := store.Create(context.Background(), Request{
		ID:             "req-processing-pending",
		CouponID:       1016,
		UserID:         2016,
		IdempotencyKey: "idem-processing-pending",
		Status:         StatusEnqueued,
	}); err != nil {
		t.Fatalf("seed request failed: %v", err)
	}
	writer := &countingClaimWriter{claimID: 9016, inserted: true}
	consumer := NewConsumer(store, writer, &fakeHotPath{})

	if err := consumer.ConsumeAccepted(context.Background(), "req-processing-pending"); err != nil {
		t.Fatalf("consumer should ignore durability pending on status updates: %v", err)
	}
	req, found, getErr := store.Get(context.Background(), "req-processing-pending")
	if getErr != nil {
		t.Fatalf("load request failed: %v", getErr)
	}
	if !found || req.Status != StatusSucceeded || req.ClaimID != 9016 {
		t.Fatalf("expected succeeded request after durability pending, got found=%v req=%+v", found, req)
	}
}

func TestConsumer_TransitionSkippedOnMarkProcessingNoops(t *testing.T) {
	store := &statusUpdateAfterSuccessErrorStore{
		RequestStore: newFakeRequestStore(),
		errorsByStatus: map[Status]error{
			StatusProcessing: TransitionSkippedError{
				RequestID: "req-processing-skipped",
				Current:   StatusSucceeded,
				Target:    StatusProcessing,
			},
		},
	}
	if err := store.Create(context.Background(), Request{
		ID:             "req-processing-skipped",
		CouponID:       1017,
		UserID:         2017,
		IdempotencyKey: "idem-processing-skipped",
		Status:         StatusSucceeded,
		ClaimID:        9017,
	}); err != nil {
		t.Fatalf("seed request failed: %v", err)
	}
	writer := &countingClaimWriter{claimID: 9018, inserted: true}
	consumer := NewConsumer(store, writer, &fakeHotPath{})

	if err := consumer.ConsumeAccepted(context.Background(), "req-processing-skipped"); err != nil {
		t.Fatalf("consumer should noop on skipped processing transition: %v", err)
	}
	if writer.calls != 0 {
		t.Fatalf("expected no persist after skipped processing transition, got %d", writer.calls)
	}
}

func TestReconciler_SkipsFreshRequests(t *testing.T) {
	now := time.Now().UTC()
	store := newFakeRequestStore()
	for _, req := range []Request{
		{ID: "req-fresh-accepted", Status: StatusAccepted, UpdatedAt: now, AcceptedAt: now},
		{ID: "req-fresh-publishing", Status: StatusPublishing, UpdatedAt: now, AcceptedAt: now},
		{ID: "req-fresh-enqueued", Status: StatusEnqueued, UpdatedAt: now, AcceptedAt: now},
		{ID: "req-fresh-processing", Status: StatusProcessing, UpdatedAt: now, AcceptedAt: now},
	} {
		if err := store.Create(context.Background(), req); err != nil {
			t.Fatalf("seed request %s failed: %v", req.ID, err)
		}
	}
	pub := &fakePublisher{}
	hotpath := &countingHotPath{}
	reconciler := NewReconciler(store, pub, hotpath, &fakeClaimLookup{}, ReconcilePolicy{
		PublishStaleAfter:    time.Minute,
		ProcessingStaleAfter: time.Minute,
	})

	if err := reconciler.ReconcileOnce(context.Background(), 10); err != nil {
		t.Fatalf("reconcile should skip fresh requests: %v", err)
	}
	if len(pub.published) != 0 {
		t.Fatalf("expected no republishes for fresh requests, got %+v", pub.published)
	}
	if hotpath.finalizeCalls != 0 {
		t.Fatalf("expected no finalize for fresh requests, got %d", hotpath.finalizeCalls)
	}
}

func TestReconciler_ProcessingLookupErrorReturnsError(t *testing.T) {
	stale := time.Now().UTC().Add(-time.Minute)
	store := newFakeRequestStore()
	if err := store.Create(context.Background(), Request{
		ID:             "req-lookup-error",
		CouponID:       1016,
		UserID:         2016,
		IdempotencyKey: "idem-lookup-error",
		Status:         StatusProcessing,
		AcceptedAt:     stale,
		UpdatedAt:      stale,
	}); err != nil {
		t.Fatalf("seed request failed: %v", err)
	}
	reconciler := NewReconciler(store, &fakePublisher{}, &fakeHotPath{}, errorClaimLookup{err: errors.New("lookup failed")}, ReconcilePolicy{
		PublishStaleAfter:    time.Second,
		ProcessingStaleAfter: time.Second,
	})

	err := reconciler.ReconcileOnce(context.Background(), 10)
	if err == nil || err.Error() != "lookup failed" {
		t.Fatalf("expected lookup failure, got %v", err)
	}
}

type countingHotPath struct {
	fakeHotPath
	decideCalls   int
	finalizeCalls int
	rollbackCalls int
}

func (h *countingHotPath) Decide(ctx context.Context, couponID, userID int64, idemKey string) (Decision, error) {
	h.decideCalls++
	return h.fakeHotPath.Decide(ctx, couponID, userID, idemKey)
}

func (h *countingHotPath) Finalize(ctx context.Context, couponID, userID int64, idemKey, requestID string, claimID int64) error {
	h.finalizeCalls++
	return h.fakeHotPath.Finalize(ctx, couponID, userID, idemKey, requestID, claimID)
}

func (h *countingHotPath) Rollback(ctx context.Context, couponID, userID int64, idemKey, requestID string) error {
	h.rollbackCalls++
	return h.fakeHotPath.Rollback(ctx, couponID, userID, idemKey, requestID)
}

type countingClaimWriter struct {
	claimID  int64
	inserted bool
	err      error
	calls    int
}

func (w *countingClaimWriter) PersistClaim(_ context.Context, _ Request) (int64, bool, error) {
	w.calls++
	return w.claimID, w.inserted, w.err
}

type createExistsStore struct {
	existing  Request
	findCalls int
}

func (s *createExistsStore) Create(context.Context, Request) error {
	return ErrRequestExists
}

func (s *createExistsStore) UpdateStatus(context.Context, string, Status, int64, string) error {
	return nil
}

func (s *createExistsStore) Get(context.Context, string) (Request, bool, error) {
	return Request{}, false, nil
}

func (s *createExistsStore) FindByIdempotency(_ context.Context, _, _ int64, _ string) (Request, bool, error) {
	s.findCalls++
	if s.findCalls == 1 {
		return Request{}, false, nil
	}
	return s.existing, true, nil
}

func (s *createExistsStore) ListByStatuses(context.Context, []Status, int) ([]Request, error) {
	return nil, nil
}

type statusUpdateErrorStore struct {
	RequestStore
	errorsByStatus map[Status]error
}

func (s *statusUpdateErrorStore) UpdateStatus(ctx context.Context, requestID string, status Status, claimID int64, failureCode string) error {
	if err, ok := s.errorsByStatus[status]; ok {
		return err
	}
	return s.RequestStore.UpdateStatus(ctx, requestID, status, claimID, failureCode)
}

type createDurabilityPendingStore struct {
	RequestStore
}

func (s *createDurabilityPendingStore) Create(ctx context.Context, req Request) error {
	if err := s.RequestStore.Create(ctx, req); err != nil {
		return err
	}
	return DurabilityPendingError{
		RequestID: req.ID,
		Status:    req.Status,
		Err:       errors.New("replica confirmation incomplete"),
	}
}

type statusUpdateAfterSuccessErrorStore struct {
	RequestStore
	errorsByStatus map[Status]error
}

func (s *statusUpdateAfterSuccessErrorStore) UpdateStatus(ctx context.Context, requestID string, status Status, claimID int64, failureCode string) error {
	if err := s.RequestStore.UpdateStatus(ctx, requestID, status, claimID, failureCode); err != nil {
		return err
	}
	if err, ok := s.errorsByStatus[status]; ok {
		return err
	}
	return nil
}

type errorClaimLookup struct {
	err error
}

func (e errorClaimLookup) FindClaimID(context.Context, Request) (int64, bool, error) {
	return 0, false, e.err
}
