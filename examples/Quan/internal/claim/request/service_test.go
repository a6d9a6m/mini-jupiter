package request

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcceptService_AdmittedRequestPersistsAndPublishes(t *testing.T) {
	store := newFakeRequestStore()
	pub := &fakePublisher{}
	hotpath := &fakeHotPath{
		decision: Decision{
			Code:      DecisionCodeAdmitted,
			RequestID: "req-001",
		},
	}
	svc := NewAcceptService(hotpath, store, pub)

	got, err := svc.Accept(context.Background(), AcceptRequest{
		CouponID:       1001,
		UserID:         2001,
		IdempotencyKey: "idem-001",
	})
	if err != nil {
		t.Fatalf("accept should succeed: %v", err)
	}
	if got.RequestID != "req-001" {
		t.Fatalf("expected request id req-001, got %q", got.RequestID)
	}
	if got.Status != StatusEnqueued {
		t.Fatalf("expected status %s, got %s", StatusEnqueued, got.Status)
	}

	req, ok, err := store.Get(context.Background(), "req-001")
	if err != nil {
		t.Fatalf("load request failed: %v", err)
	}
	if !ok {
		t.Fatal("expected request to be persisted in store")
	}
	if req.Status != StatusEnqueued {
		t.Fatalf("expected persisted request status %s, got %s", StatusEnqueued, req.Status)
	}
	if len(pub.published) != 1 || pub.published[0] != "req-001" {
		t.Fatalf("expected published request req-001, got %+v", pub.published)
	}
}

func TestAcceptService_PublishFailureLeavesRecoverableRequest(t *testing.T) {
	store := newFakeRequestStore()
	pub := &fakePublisher{err: errors.New("mq unavailable")}
	hotpath := &fakeHotPath{
		decision: Decision{
			Code:      DecisionCodeAdmitted,
			RequestID: "req-002",
		},
	}
	svc := NewAcceptService(hotpath, store, pub)

	got, err := svc.Accept(context.Background(), AcceptRequest{
		CouponID:       1002,
		UserID:         2002,
		IdempotencyKey: "idem-002",
	})
	if err != nil {
		t.Fatalf("accept should still return recoverable acceptance on publish failure: %v", err)
	}
	if got.Status != StatusPublishing {
		t.Fatalf("expected status %s, got %s", StatusPublishing, got.Status)
	}

	req, ok, err := store.Get(context.Background(), "req-002")
	if err != nil {
		t.Fatalf("load request failed: %v", err)
	}
	if !ok {
		t.Fatal("expected request to exist after publish failure")
	}
	if req.Status != StatusPublishing {
		t.Fatalf("expected persisted request status %s, got %s", StatusPublishing, req.Status)
	}
	if len(hotpath.rolledBack) != 0 {
		t.Fatalf("request should not rollback on first publish failure, got %+v", hotpath.rolledBack)
	}
}

func TestAcceptService_RejectedRequestDoesNotPersistOrPublish(t *testing.T) {
	store := newFakeRequestStore()
	pub := &fakePublisher{}
	hotpath := &fakeHotPath{
		decision: Decision{
			Code: DecisionCodeRejected,
		},
	}
	svc := NewAcceptService(hotpath, store, pub)

	if _, err := svc.Accept(context.Background(), AcceptRequest{
		CouponID:       1002,
		UserID:         2002,
		IdempotencyKey: "idem-rejected",
	}); err == nil {
		t.Fatal("expected rejected request to return error")
	}
	if len(store.requests) != 0 {
		t.Fatalf("expected no request to be stored, got %+v", store.requests)
	}
	if len(pub.published) != 0 {
		t.Fatalf("expected no request to be published, got %+v", pub.published)
	}
}

func TestConsumer_ConsumeAcceptedPersistsClaimAndFinalizes(t *testing.T) {
	store := newFakeRequestStore()
	_ = store.Create(context.Background(), Request{
		ID:             "req-003",
		CouponID:       1003,
		UserID:         2003,
		IdempotencyKey: "idem-003",
		ReservationID:  "req-003",
		Status:         StatusEnqueued,
	})
	writer := &fakeClaimWriter{claimID: 7788, inserted: true}
	hotpath := &fakeHotPath{}
	consumer := NewConsumer(store, writer, hotpath)

	if err := consumer.ConsumeAccepted(context.Background(), "req-003"); err != nil {
		t.Fatalf("consume should succeed: %v", err)
	}

	req, ok, err := store.Get(context.Background(), "req-003")
	if err != nil {
		t.Fatalf("load request failed: %v", err)
	}
	if !ok {
		t.Fatal("expected request to remain in store")
	}
	if req.Status != StatusSucceeded {
		t.Fatalf("expected status %s, got %s", StatusSucceeded, req.Status)
	}
	if req.ClaimID != 7788 {
		t.Fatalf("expected claim id 7788, got %d", req.ClaimID)
	}
	if len(hotpath.finalized) != 1 || hotpath.finalized[0] != "req-003" {
		t.Fatalf("expected finalize for req-003, got %+v", hotpath.finalized)
	}
}

func TestConsumer_DuplicateDeliveryReusesExistingClaim(t *testing.T) {
	store := newFakeRequestStore()
	_ = store.Create(context.Background(), Request{
		ID:             "req-004",
		CouponID:       1004,
		UserID:         2004,
		IdempotencyKey: "idem-004",
		ReservationID:  "req-004",
		Status:         StatusEnqueued,
	})
	writer := &fakeClaimWriter{claimID: 8800, inserted: false}
	hotpath := &fakeHotPath{}
	consumer := NewConsumer(store, writer, hotpath)

	if err := consumer.ConsumeAccepted(context.Background(), "req-004"); err != nil {
		t.Fatalf("duplicate consume should still converge successfully: %v", err)
	}

	req, ok, err := store.Get(context.Background(), "req-004")
	if err != nil {
		t.Fatalf("load request failed: %v", err)
	}
	if !ok {
		t.Fatal("expected request to remain in store")
	}
	if req.Status != StatusSucceeded {
		t.Fatalf("expected status %s, got %s", StatusSucceeded, req.Status)
	}
	if req.ClaimID != 8800 {
		t.Fatalf("expected claim id 8800, got %d", req.ClaimID)
	}
}

func TestConsumer_FinalizeFailureLeavesRequestRecoverableUntilReconciled(t *testing.T) {
	store := newFakeRequestStore()
	_ = store.Create(context.Background(), Request{
		ID:             "req-004a",
		CouponID:       1004,
		UserID:         2004,
		IdempotencyKey: "idem-004a",
		ReservationID:  "req-004a",
		Status:         StatusEnqueued,
	})
	writer := &fakeClaimWriter{claimID: 8700, inserted: true}
	hotpath := &fakeHotPath{finalizeErr: errors.New("redis finalize timeout")}
	consumer := NewConsumer(store, writer, hotpath)

	if err := consumer.ConsumeAccepted(context.Background(), "req-004a"); err == nil {
		t.Fatal("expected finalize failure to bubble up")
	}

	req, ok, err := store.Get(context.Background(), "req-004a")
	if err != nil {
		t.Fatalf("load request failed: %v", err)
	}
	if !ok {
		t.Fatal("expected request to remain in store")
	}
	if req.Status != StatusProcessing {
		t.Fatalf("expected status %s, got %s", StatusProcessing, req.Status)
	}
	if req.ClaimID != 0 {
		t.Fatalf("expected claim id to stay unset before finalize succeeds, got %d", req.ClaimID)
	}

	stale := time.Now().UTC().Add(-time.Minute)
	req.UpdatedAt = stale
	store.requests[req.ID] = req
	hotpath.finalizeErr = nil
	reconciler := NewReconciler(store, &fakePublisher{}, hotpath, &fakeClaimLookup{
		claims: map[string]int64{"req-004a": 8700},
	}, ReconcilePolicy{
		PublishStaleAfter:    time.Second,
		ProcessingStaleAfter: time.Second,
	})

	if err := reconciler.ReconcileOnce(context.Background(), 10); err != nil {
		t.Fatalf("reconcile should finish finalize recovery: %v", err)
	}

	req, _, _ = store.Get(context.Background(), "req-004a")
	if req.Status != StatusSucceeded || req.ClaimID != 8700 {
		t.Fatalf("expected req-004a to become succeeded with claim 8700, got %+v", req)
	}
}

func TestConsumer_PersistFailureRollsBackAndMarksTerminalFailure(t *testing.T) {
	store := newFakeRequestStore()
	_ = store.Create(context.Background(), Request{
		ID:             "req-004b",
		CouponID:       1004,
		UserID:         2004,
		IdempotencyKey: "idem-004b",
		ReservationID:  "req-004b",
		Status:         StatusEnqueued,
	})
	writer := &fakeClaimWriter{err: errors.New("sql unavailable")}
	hotpath := &fakeHotPath{}
	consumer := NewConsumer(store, writer, hotpath)

	if err := consumer.ConsumeAccepted(context.Background(), "req-004b"); err != nil {
		t.Fatalf("consumer should converge non-retriable persist failure into a terminal state: %v", err)
	}

	req, ok, err := store.Get(context.Background(), "req-004b")
	if err != nil {
		t.Fatalf("load request failed: %v", err)
	}
	if !ok {
		t.Fatal("expected request to remain in store")
	}
	if req.Status != StatusRolledBack {
		t.Fatalf("expected status %s, got %s", StatusRolledBack, req.Status)
	}
	if len(hotpath.rolledBack) != 1 || hotpath.rolledBack[0] != "req-004b" {
		t.Fatalf("expected rollback for req-004b, got %+v", hotpath.rolledBack)
	}
}

func TestConsumer_RollbackFailureLeavesRequestRecoverableUntilReconciled(t *testing.T) {
	store := newFakeRequestStore()
	_ = store.Create(context.Background(), Request{
		ID:             "req-004c",
		CouponID:       1004,
		UserID:         2004,
		IdempotencyKey: "idem-004c",
		ReservationID:  "req-004c",
		Status:         StatusEnqueued,
	})
	writer := &fakeClaimWriter{err: errors.New("sql unavailable")}
	hotpath := &fakeHotPath{rollbackErr: errors.New("redis rollback timeout")}
	consumer := NewConsumer(store, writer, hotpath)

	if err := consumer.ConsumeAccepted(context.Background(), "req-004c"); err == nil {
		t.Fatal("expected rollback failure to bubble up")
	}

	req, ok, err := store.Get(context.Background(), "req-004c")
	if err != nil {
		t.Fatalf("load request failed: %v", err)
	}
	if !ok {
		t.Fatal("expected request to remain in store")
	}
	if req.Status != StatusProcessing {
		t.Fatalf("expected status %s, got %s", StatusProcessing, req.Status)
	}

	stale := time.Now().UTC().Add(-time.Minute)
	req.UpdatedAt = stale
	store.requests[req.ID] = req
	hotpath.rollbackErr = nil
	pub := &fakePublisher{}
	reconciler := NewReconciler(store, pub, hotpath, &fakeClaimLookup{
		claims: map[string]int64{},
	}, ReconcilePolicy{
		PublishStaleAfter:    time.Second,
		ProcessingStaleAfter: time.Second,
	})

	if err := reconciler.ReconcileOnce(context.Background(), 10); err != nil {
		t.Fatalf("reconcile should finish rollback recovery: %v", err)
	}

	req, _, _ = store.Get(context.Background(), "req-004c")
	if req.Status != StatusEnqueued {
		t.Fatalf("expected req-004c to become enqueued for replay, got %+v", req)
	}
	if len(pub.published) != 1 || pub.published[0] != "req-004c" {
		t.Fatalf("expected req-004c to be republished, got %+v", pub.published)
	}
}

func TestQueryService_MapsIntermediateAndTerminalStatuses(t *testing.T) {
	store := newFakeRequestStore()
	_ = store.Create(context.Background(), Request{ID: "req-005", Status: StatusAccepted})
	_ = store.Create(context.Background(), Request{ID: "req-006", Status: StatusSucceeded, ClaimID: 9900})
	_ = store.Create(context.Background(), Request{ID: "req-007", Status: StatusFailed, FailureCode: "publish_failed"})
	svc := NewQueryService(store)

	processing, err := svc.Get(context.Background(), "req-005")
	if err != nil {
		t.Fatalf("query processing request failed: %v", err)
	}
	if processing.State != ResultStateProcessing {
		t.Fatalf("expected processing state, got %s", processing.State)
	}

	success, err := svc.Get(context.Background(), "req-006")
	if err != nil {
		t.Fatalf("query success request failed: %v", err)
	}
	if success.State != ResultStateSucceeded || success.ClaimID != 9900 {
		t.Fatalf("expected succeeded with claim 9900, got %+v", success)
	}

	failed, err := svc.Get(context.Background(), "req-007")
	if err != nil {
		t.Fatalf("query failed request failed: %v", err)
	}
	if failed.State != ResultStateFailed || failed.FailureCode != "publish_failed" {
		t.Fatalf("expected failed result, got %+v", failed)
	}
}

func TestReconciler_RepublishesAcceptedRequestsAndRepairsCompletedOnes(t *testing.T) {
	store := newFakeRequestStore()
	stale := time.Now().UTC().Add(-time.Minute)
	_ = store.Create(context.Background(), Request{
		ID:             "req-008",
		CouponID:       1008,
		UserID:         2008,
		IdempotencyKey: "idem-008",
		Status:         StatusAccepted,
		AcceptedAt:     stale,
		UpdatedAt:      stale,
	})
	_ = store.Create(context.Background(), Request{
		ID:             "req-009",
		CouponID:       1009,
		UserID:         2009,
		IdempotencyKey: "idem-009",
		Status:         StatusProcessing,
		AcceptedAt:     stale,
		UpdatedAt:      stale,
	})
	_ = store.Create(context.Background(), Request{
		ID:             "req-009b",
		CouponID:       1009,
		UserID:         2019,
		IdempotencyKey: "idem-009b",
		Status:         StatusEnqueued,
		AcceptedAt:     stale,
		UpdatedAt:      stale,
	})
	pub := &fakePublisher{}
	hotpath := &fakeHotPath{}
	claims := &fakeClaimLookup{
		claims: map[string]int64{"req-009": 9911},
	}
	reconciler := NewReconciler(store, pub, hotpath, claims, ReconcilePolicy{
		PublishStaleAfter:    time.Second,
		ProcessingStaleAfter: time.Second,
	})

	if err := reconciler.ReconcileOnce(context.Background(), 10); err != nil {
		t.Fatalf("reconcile should converge stale requests: %v", err)
	}

	reqAccepted, _, _ := store.Get(context.Background(), "req-008")
	if reqAccepted.Status != StatusEnqueued {
		t.Fatalf("expected req-008 to become %s, got %s", StatusEnqueued, reqAccepted.Status)
	}
	reqCompleted, _, _ := store.Get(context.Background(), "req-009")
	if reqCompleted.Status != StatusSucceeded || reqCompleted.ClaimID != 9911 {
		t.Fatalf("expected req-009 to become succeeded with claim 9911, got %+v", reqCompleted)
	}
	reqRepublished, _, _ := store.Get(context.Background(), "req-009b")
	if reqRepublished.Status != StatusEnqueued {
		t.Fatalf("expected req-009b to remain %s, got %s", StatusEnqueued, reqRepublished.Status)
	}
	if len(hotpath.finalized) != 1 || hotpath.finalized[0] != "req-009" {
		t.Fatalf("expected finalize for req-009, got %+v", hotpath.finalized)
	}
	if len(pub.published) != 2 {
		t.Fatalf("expected two stale requests to be republished, got %+v", pub.published)
	}
}

func TestReconciler_RepublishesProcessingRequestsWithoutPersistedClaim(t *testing.T) {
	store := newFakeRequestStore()
	stale := time.Now().UTC().Add(-time.Minute)
	_ = store.Create(context.Background(), Request{
		ID:             "req-010",
		CouponID:       1010,
		UserID:         2010,
		IdempotencyKey: "idem-010",
		Status:         StatusProcessing,
		AcceptedAt:     stale,
		UpdatedAt:      stale,
	})
	pub := &fakePublisher{}
	hotpath := &fakeHotPath{}
	claims := &fakeClaimLookup{claims: map[string]int64{}}
	reconciler := NewReconciler(store, pub, hotpath, claims, ReconcilePolicy{
		PublishStaleAfter:    time.Second,
		ProcessingStaleAfter: time.Second,
	})

	if err := reconciler.ReconcileOnce(context.Background(), 10); err != nil {
		t.Fatalf("reconcile should republish orphaned processing requests: %v", err)
	}

	req, _, _ := store.Get(context.Background(), "req-010")
	if req.Status != StatusEnqueued {
		t.Fatalf("expected req-010 to become %s, got %s", StatusEnqueued, req.Status)
	}
	if len(pub.published) != 1 || pub.published[0] != "req-010" {
		t.Fatalf("expected req-010 to be republished, got %+v", pub.published)
	}
}

type fakeHotPath struct {
	decision    Decision
	decideErr   error
	finalizeErr error
	rollbackErr error
	finalized   []string
	rolledBack  []string
}

func (f *fakeHotPath) Decide(_ context.Context, _, _ int64, _ string) (Decision, error) {
	return f.decision, f.decideErr
}

func (f *fakeHotPath) Finalize(_ context.Context, _, _ int64, _ string, requestID string, _ int64) error {
	if f.finalizeErr != nil {
		return f.finalizeErr
	}
	f.finalized = append(f.finalized, requestID)
	return nil
}

func (f *fakeHotPath) Rollback(_ context.Context, _, _ int64, _ string, requestID string) error {
	if f.rollbackErr != nil {
		return f.rollbackErr
	}
	f.rolledBack = append(f.rolledBack, requestID)
	return nil
}

type fakeRequestStore struct {
	requests map[string]Request
}

func newFakeRequestStore() *fakeRequestStore {
	return &fakeRequestStore{requests: map[string]Request{}}
}

func (f *fakeRequestStore) Create(_ context.Context, req Request) error {
	now := time.Now().UTC()
	if req.AcceptedAt.IsZero() {
		req.AcceptedAt = now
	}
	if req.UpdatedAt.IsZero() {
		req.UpdatedAt = now
	}
	if req.Version == 0 {
		req.Version = 1
	}
	f.requests[req.ID] = req
	return nil
}

func (f *fakeRequestStore) UpdateStatus(_ context.Context, requestID string, status Status, claimID int64, failureCode string) error {
	req := f.requests[requestID]
	if isTerminalStatus(req.Status) && !isTerminalStatus(status) {
		return nil
	}
	now := time.Now().UTC()
	req.Status = status
	req.Version++
	if claimID != 0 {
		req.ClaimID = claimID
	}
	req.FailureCode = failureCode
	req.UpdatedAt = now
	if status == StatusProcessing {
		req.ProcessedAt = now
	}
	if status == StatusSucceeded || status == StatusRolledBack || status == StatusFailed {
		req.FinishedAt = now
	}
	f.requests[requestID] = req
	return nil
}

func (f *fakeRequestStore) CompareAndUpdateStatus(ctx context.Context, snapshot Request, status Status, claimID int64, failureCode string) (bool, error) {
	req, ok := f.requests[snapshot.ID]
	if !ok {
		return false, ErrRequestNotFound
	}
	if req.Status != snapshot.Status || req.Version != snapshot.Version {
		return false, nil
	}
	return true, f.UpdateStatus(ctx, snapshot.ID, status, claimID, failureCode)
}

func (f *fakeRequestStore) Get(_ context.Context, requestID string) (Request, bool, error) {
	req, ok := f.requests[requestID]
	return req, ok, nil
}

func (f *fakeRequestStore) FindByIdempotency(_ context.Context, couponID, userID int64, idemKey string) (Request, bool, error) {
	for _, req := range f.requests {
		if req.CouponID == couponID && req.UserID == userID && req.IdempotencyKey == idemKey {
			return req, true, nil
		}
	}
	return Request{}, false, nil
}

func (f *fakeRequestStore) ListByStatuses(_ context.Context, statuses []Status, limit int) ([]Request, error) {
	if limit <= 0 {
		limit = len(f.requests)
	}
	allowed := make(map[Status]struct{}, len(statuses))
	for _, status := range statuses {
		allowed[status] = struct{}{}
	}
	out := make([]Request, 0, limit)
	for _, req := range f.requests {
		if _, ok := allowed[req.Status]; !ok {
			continue
		}
		out = append(out, req)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

type fakePublisher struct {
	err       error
	published []string
}

func (f *fakePublisher) PublishAccepted(_ context.Context, req Request) error {
	if f.err != nil {
		return f.err
	}
	f.published = append(f.published, req.ID)
	return nil
}

type fakeClaimWriter struct {
	claimID  int64
	inserted bool
	err      error
}

func (f *fakeClaimWriter) PersistClaim(_ context.Context, _ Request) (int64, bool, error) {
	return f.claimID, f.inserted, f.err
}

type fakeClaimLookup struct {
	claims map[string]int64
}

func (f *fakeClaimLookup) FindClaimID(_ context.Context, req Request) (int64, bool, error) {
	claimID, ok := f.claims[req.ID]
	return claimID, ok, nil
}
