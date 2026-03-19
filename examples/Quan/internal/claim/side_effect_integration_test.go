package claim

import (
	"context"
	"strconv"
	"testing"
	"time"

	"mini-jupiter/examples/Quan/internal/outbox"
	"mini-jupiter/examples/Quan/internal/task"
	"mini-jupiter/pkg/mysql"
)

func TestRepository_ClaimCreatesPendingSideEffectWithoutInlineTaskOutbox(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 5, 1)

	repo := newIntegrationRepository(t, db)
	baseTasks := countAsyncTasksTotal(t, db)
	baseOutbox := countOutboxEventsTotal(t, db)

	rec, err := repo.ClaimCoupon(ctx, couponID, 96001, "side-effect-pending")
	if err != nil {
		t.Fatalf("claim coupon failed: %v", err)
	}

	effect := loadClaimSideEffectByClaim(t, db, rec.ID)
	if effect.Status != ClaimSideEffectStatusPending {
		t.Fatalf("expected side effect pending, got %q", effect.Status)
	}
	if effect.AsyncTaskID != 0 || effect.OutboxEventID != 0 {
		t.Fatalf("expected side effect task/outbox ids to be empty, got task=%d outbox=%d", effect.AsyncTaskID, effect.OutboxEventID)
	}
	payload, err := ParseClaimSideEffectPayload(effect.PayloadJSON)
	if err != nil {
		t.Fatalf("parse side effect payload failed: %v", err)
	}
	if payload.ClaimID != rec.ID || payload.CouponID != rec.CouponID || payload.UserID != rec.UserID {
		t.Fatalf("unexpected side effect payload: %+v", payload)
	}
	if got := countAsyncTasksTotal(t, db); got != baseTasks {
		t.Fatalf("expected async task count unchanged before dispatcher, got base=%d current=%d", baseTasks, got)
	}
	if got := countOutboxEventsTotal(t, db); got != baseOutbox {
		t.Fatalf("expected outbox count unchanged before dispatcher, got base=%d current=%d", baseOutbox, got)
	}
}

func TestSideEffectDispatcher_DispatchesPendingClaimSideEffect(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 5, 1)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	sideEffectRepo := NewSideEffectRepository(db)
	repo := NewRepository(db, txm, sideEffectRepo)
	taskRepo := task.NewRepository(db, txm)
	outboxRepo := outbox.NewRepository(db)
	dispatcher, err := NewSideEffectDispatcher(sideEffectRepo, taskRepo, outboxRepo, SideEffectDispatchConfig{
		Enabled:   true,
		BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("new side effect dispatcher failed: %v", err)
	}

	baseTasks := countAsyncTasksTotal(t, db)
	baseOutbox := countOutboxEventsTotal(t, db)
	rec, err := repo.ClaimCoupon(ctx, couponID, 96002, "side-effect-dispatch")
	if err != nil {
		t.Fatalf("claim coupon failed: %v", err)
	}
	if err := dispatcher.recoverAndDispatchOnce(ctx); err != nil {
		t.Fatalf("dispatch once failed: %v", err)
	}

	effect := loadClaimSideEffectByClaim(t, db, rec.ID)
	if effect.Status != ClaimSideEffectStatusDone {
		t.Fatalf("expected side effect done, got %q", effect.Status)
	}
	if effect.AsyncTaskID <= 0 {
		t.Fatalf("expected async task id, got %d", effect.AsyncTaskID)
	}
	if effect.OutboxEventID <= 0 {
		t.Fatalf("expected outbox event id, got %d", effect.OutboxEventID)
	}
	if got := countAsyncTasksTotal(t, db); got != baseTasks+1 {
		t.Fatalf("expected async task count base+1, got base=%d current=%d", baseTasks, got)
	}
	if got := countOutboxEventsTotal(t, db); got != baseOutbox+1 {
		t.Fatalf("expected outbox count base+1, got base=%d current=%d", baseOutbox, got)
	}

	taskRec, err := taskRepo.GetByTypeBiz(ctx, task.TaskTypeSendCouponNotice, "claim:"+itoa(rec.ID))
	if err != nil {
		t.Fatalf("load dispatched task failed: %v", err)
	}
	if taskRec.ID != effect.AsyncTaskID {
		t.Fatalf("expected task id %d, got %d", effect.AsyncTaskID, taskRec.ID)
	}
	outboxEvt, found, err := outboxRepo.FindByAggregate(ctx, outbox.EventTypeTaskCreated, "claim_side_effect", itoa(effect.ID))
	if err != nil {
		t.Fatalf("load outbox event failed: %v", err)
	}
	if !found {
		t.Fatal("expected outbox event to exist")
	}
	if outboxEvt.ID != effect.OutboxEventID {
		t.Fatalf("expected outbox id %d, got %d", effect.OutboxEventID, outboxEvt.ID)
	}
}

func TestSideEffectDispatcher_RepeatedDispatchDoesNotDuplicateTaskOrOutbox(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 5, 1)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	sideEffectRepo := NewSideEffectRepository(db)
	repo := NewRepository(db, txm, sideEffectRepo)
	taskRepo := task.NewRepository(db, txm)
	outboxRepo := outbox.NewRepository(db)
	dispatcher, err := NewSideEffectDispatcher(sideEffectRepo, taskRepo, outboxRepo, SideEffectDispatchConfig{
		Enabled:   true,
		BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("new side effect dispatcher failed: %v", err)
	}

	rec, err := repo.ClaimCoupon(ctx, couponID, 96003, "side-effect-repeat")
	if err != nil {
		t.Fatalf("claim coupon failed: %v", err)
	}
	if err := dispatcher.recoverAndDispatchOnce(ctx); err != nil {
		t.Fatalf("first dispatch failed: %v", err)
	}
	effect := loadClaimSideEffectByClaim(t, db, rec.ID)
	firstTaskCount := countAsyncTasksTotal(t, db)
	firstOutboxCount := countOutboxEventsTotal(t, db)

	if err := dispatcher.recoverAndDispatchOnce(ctx); err != nil {
		t.Fatalf("second dispatch failed: %v", err)
	}
	again := loadClaimSideEffectByClaim(t, db, rec.ID)
	if again.AsyncTaskID != effect.AsyncTaskID || again.OutboxEventID != effect.OutboxEventID {
		t.Fatalf("expected same task/outbox ids after repeated dispatch, got before task=%d outbox=%d after task=%d outbox=%d", effect.AsyncTaskID, effect.OutboxEventID, again.AsyncTaskID, again.OutboxEventID)
	}
	if got := countAsyncTasksTotal(t, db); got != firstTaskCount {
		t.Fatalf("expected no duplicate async task, got first=%d current=%d", firstTaskCount, got)
	}
	if got := countOutboxEventsTotal(t, db); got != firstOutboxCount {
		t.Fatalf("expected no duplicate outbox event, got first=%d current=%d", firstOutboxCount, got)
	}
}

func TestSideEffectDispatcher_RecoversStaleProcessingThenDispatches(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 5, 1)

	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	sideEffectRepo := NewSideEffectRepository(db)
	repo := NewRepository(db, txm, sideEffectRepo)
	taskRepo := task.NewRepository(db, txm)
	outboxRepo := outbox.NewRepository(db)
	dispatcher, err := NewSideEffectDispatcher(sideEffectRepo, taskRepo, outboxRepo, SideEffectDispatchConfig{
		Enabled:      true,
		BatchSize:    10,
		StaleTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new side effect dispatcher failed: %v", err)
	}

	rec, err := repo.ClaimCoupon(ctx, couponID, 96004, "side-effect-stale")
	if err != nil {
		t.Fatalf("claim coupon failed: %v", err)
	}
	effect := loadClaimSideEffectByClaim(t, db, rec.ID)
	if _, err := db.Exec(`
UPDATE claim_side_effects
SET status = ?, updated_at = ?
WHERE side_effect_id = ?
`, ClaimSideEffectStatusProcessing, time.Now().UTC().Add(-time.Second), effect.ID); err != nil {
		t.Fatalf("mark side effect stale processing failed: %v", err)
	}

	if err := dispatcher.recoverAndDispatchOnce(ctx); err != nil {
		t.Fatalf("recover and dispatch failed: %v", err)
	}
	recovered := loadClaimSideEffectByClaim(t, db, rec.ID)
	if recovered.Status != ClaimSideEffectStatusDone {
		t.Fatalf("expected recovered side effect done, got %q", recovered.Status)
	}
	if recovered.AsyncTaskID <= 0 || recovered.OutboxEventID <= 0 {
		t.Fatalf("expected recovered side effect to have task/outbox ids, got task=%d outbox=%d", recovered.AsyncTaskID, recovered.OutboxEventID)
	}
}

func TestSideEffectDispatcher_SuspendsMalformedPayloadAtRetryBoundary(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	txm, err := mysql.NewTxManager(db)
	if err != nil {
		t.Fatalf("new tx manager failed: %v", err)
	}
	sideEffectRepo := NewSideEffectRepository(db)
	taskRepo := task.NewRepository(db, txm)
	outboxRepo := outbox.NewRepository(db)
	dispatcher, err := NewSideEffectDispatcher(sideEffectRepo, taskRepo, outboxRepo, SideEffectDispatchConfig{
		Enabled:   true,
		BatchSize: 10,
		MaxRetry:  1,
	})
	if err != nil {
		t.Fatalf("new side effect dispatcher failed: %v", err)
	}

	couponID := nextTestCouponID()
	resetTestData(t, db, couponID)
	createCampaign(t, db, couponID, 5, 1)
	repo := NewRepository(db, txm, sideEffectRepo)
	rec, err := repo.ClaimCoupon(ctx, couponID, 96005, "side-effect-suspend")
	if err != nil {
		t.Fatalf("claim coupon failed: %v", err)
	}
	effect := loadClaimSideEffectByClaim(t, db, rec.ID)
	if _, err := db.Exec(`
UPDATE claim_side_effects
SET payload_json = ?
WHERE side_effect_id = ?
`, `"bad"`, effect.ID); err != nil {
		t.Fatalf("corrupt side effect payload failed: %v", err)
	}

	if err := dispatcher.recoverAndDispatchOnce(ctx); err != nil {
		t.Fatalf("dispatch malformed payload failed: %v", err)
	}
	suspended := loadClaimSideEffectByClaim(t, db, rec.ID)
	if suspended.Status != ClaimSideEffectStatusSuspended {
		t.Fatalf("expected suspended status, got %q", suspended.Status)
	}
	if suspended.RetryCount != 1 {
		t.Fatalf("expected retry count 1 at suspend boundary, got %d", suspended.RetryCount)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
