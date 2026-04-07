package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	claimrequest "mini-jupiter/examples/Quan/internal/claim/request"
	"mini-jupiter/examples/Quan/internal/testutil/quanenv"
	appredis "mini-jupiter/pkg/redis"
)

func TestLoadBenchmarkAcceptedDeltaVsRequests(t *testing.T) {
	reportPath := writeBenchmarkReport(t, reportEnvelope{
		Scenario: "unit-benchmark",
		Business: struct {
			Success int `json:"success"`
			Total   int `json:"total"`
		}{Success: 3, Total: 5},
		HTTPStatusCounts: map[string]int{"202": 3, "409": 2},
		TransportErrors:  map[string]int{"timeout": 1},
	})

	audit, err := loadBenchmark(reportPath, &requestAudit{Total: 2})
	if err != nil {
		t.Fatalf("load benchmark failed: %v", err)
	}
	if audit.AcceptedResponses != 3 || audit.ConflictResponses != 2 || audit.TransportErrors != 1 || audit.AcceptedDeltaVsRequests != 1 {
		t.Fatalf("unexpected benchmark audit: %+v", audit)
	}
}

func TestLoadRequests_AuditsRequestLedger(t *testing.T) {
	redisClient := quanenv.OpenIntegrationRedis(t, 15)
	prefix := "audit:requests:" + time.Now().UTC().Format("150405.000000000")
	store := mustNewAuditRequestStore(t, redisClient, prefix)
	ctx := context.Background()

	mustCreateAndUpdateRequest(t, ctx, store, claimrequest.Request{
		ID:             "req-audit-1",
		CouponID:       1201,
		UserID:         2201,
		IdempotencyKey: "idem-audit-1",
		ReservationID:  "res-audit-1",
		Status:         claimrequest.StatusAccepted,
	}, claimrequest.StatusSucceeded, 9201, "")
	mustCreateAndUpdateRequest(t, ctx, store, claimrequest.Request{
		ID:             "req-audit-2",
		CouponID:       1201,
		UserID:         2202,
		IdempotencyKey: "idem-audit-2",
		ReservationID:  "res-audit-2",
		Status:         claimrequest.StatusAccepted,
	}, claimrequest.StatusSucceeded, 0, "")
	mustCreateAndUpdateRequest(t, ctx, store, claimrequest.Request{
		ID:             "req-audit-3",
		CouponID:       1201,
		UserID:         2203,
		IdempotencyKey: "idem-audit-3",
		ReservationID:  "res-audit-3",
		Status:         claimrequest.StatusAccepted,
	}, claimrequest.StatusRolledBack, 9303, "persist_failed")
	if err := store.Create(ctx, claimrequest.Request{
		ID:             "req-audit-4",
		CouponID:       1201,
		UserID:         2204,
		IdempotencyKey: "idem-audit-4",
		ReservationID:  "res-audit-4",
		Status:         claimrequest.StatusAccepted,
	}); err != nil {
		t.Fatalf("create in-flight request failed: %v", err)
	}
	staleMs := time.Now().Add(-time.Minute).UnixMilli()
	if err := redisClient.Raw().HSet(ctx, requestKey(prefix, "req-audit-4"), "updated_at_ms", staleMs).Err(); err != nil {
		t.Fatalf("mark stale request failed: %v", err)
	}

	audit, err := loadRequests(ctx, redisClient, 1201, prefix, 10*time.Second, 1)
	if err != nil {
		t.Fatalf("load requests failed: %v", err)
	}
	if audit.Total != 4 || audit.Succeeded != 2 || audit.RolledBack != 1 || audit.InFlight != 1 {
		t.Fatalf("unexpected request totals: %+v", audit)
	}
	if audit.StaleInFlight != 1 || audit.SucceededWithoutClaim != 1 || audit.TerminalFailureWithClaim != 1 {
		t.Fatalf("unexpected request anomaly counts: %+v", audit)
	}
	if audit.SuccessDeltaVsClaims != 1 {
		t.Fatalf("expected success delta vs claims 1, got %d", audit.SuccessDeltaVsClaims)
	}
}

func TestRunAudit_PassesWhenLedgerAndClaimsMatch(t *testing.T) {
	db := quanenv.OpenIntegrationDB(t, "benchaudit_pass")
	redisClient := quanenv.OpenIntegrationRedis(t, 16)
	couponID := quanenv.NextCouponID()
	prefix := "audit:run:pass:" + time.Now().UTC().Format("150405.000000000")
	ctx := context.Background()

	quanenv.CreateCampaign(t, db, couponID, 2, 1)
	insertClaim(t, db, couponID, 2301, "idem-pass-1")
	insertClaim(t, db, couponID, 2302, "idem-pass-2")

	store := mustNewAuditRequestStore(t, redisClient, prefix)
	mustCreateAndUpdateRequest(t, ctx, store, claimrequest.Request{
		ID:             "req-pass-1",
		CouponID:       couponID,
		UserID:         2301,
		IdempotencyKey: "idem-pass-1",
		ReservationID:  "res-pass-1",
		Status:         claimrequest.StatusAccepted,
	}, claimrequest.StatusSucceeded, 1, "")
	mustCreateAndUpdateRequest(t, ctx, store, claimrequest.Request{
		ID:             "req-pass-2",
		CouponID:       couponID,
		UserID:         2302,
		IdempotencyKey: "idem-pass-2",
		ReservationID:  "res-pass-2",
		Status:         claimrequest.StatusAccepted,
	}, claimrequest.StatusSucceeded, 2, "")

	reportPath := writeBenchmarkReport(t, reportEnvelope{
		Scenario: "audit-pass",
		Business: struct {
			Success int `json:"success"`
			Total   int `json:"total"`
		}{Success: 2, Total: 2},
		HTTPStatusCounts: map[string]int{"202": 2},
	})

	result, err := runAudit(ctx, db, redisClient, couponID, prefix, 30*time.Second, reportPath)
	if err != nil {
		t.Fatalf("run audit failed: %v", err)
	}
	if !result.Verdict.Pass {
		t.Fatalf("expected audit to pass, got %+v", result.Verdict)
	}
	if result.Requests == nil || result.Requests.Total != 2 || result.Requests.SuccessDeltaVsClaims != 0 {
		t.Fatalf("unexpected request audit: %+v", result.Requests)
	}
	if result.Benchmark == nil || result.Benchmark.AcceptedDeltaVsRequests != 0 {
		t.Fatalf("unexpected benchmark audit: %+v", result.Benchmark)
	}
}

func TestRunAudit_FailsOnRequestLedgerMismatch(t *testing.T) {
	db := quanenv.OpenIntegrationDB(t, "benchaudit_fail")
	redisClient := quanenv.OpenIntegrationRedis(t, 17)
	couponID := quanenv.NextCouponID()
	prefix := "audit:run:fail:" + time.Now().UTC().Format("150405.000000000")
	ctx := context.Background()

	quanenv.CreateCampaign(t, db, couponID, 2, 1)
	insertClaim(t, db, couponID, 2401, "idem-fail-1")

	store := mustNewAuditRequestStore(t, redisClient, prefix)
	mustCreateAndUpdateRequest(t, ctx, store, claimrequest.Request{
		ID:             "req-fail-1",
		CouponID:       couponID,
		UserID:         2401,
		IdempotencyKey: "idem-fail-1",
		ReservationID:  "res-fail-1",
		Status:         claimrequest.StatusAccepted,
	}, claimrequest.StatusSucceeded, 0, "")
	if err := store.Create(ctx, claimrequest.Request{
		ID:             "req-fail-2",
		CouponID:       couponID,
		UserID:         2402,
		IdempotencyKey: "idem-fail-2",
		ReservationID:  "res-fail-2",
		Status:         claimrequest.StatusAccepted,
	}); err != nil {
		t.Fatalf("create stale request failed: %v", err)
	}
	staleMs := time.Now().Add(-time.Minute).UnixMilli()
	if err := redisClient.Raw().HSet(ctx, requestKey(prefix, "req-fail-2"), "updated_at_ms", staleMs).Err(); err != nil {
		t.Fatalf("mark stale request failed: %v", err)
	}

	reportPath := writeBenchmarkReport(t, reportEnvelope{
		Scenario: "audit-fail",
		Business: struct {
			Success int `json:"success"`
			Total   int `json:"total"`
		}{Success: 2, Total: 2},
		HTTPStatusCounts: map[string]int{"202": 2},
	})

	result, err := runAudit(ctx, db, redisClient, couponID, prefix, 5*time.Second, reportPath)
	if err != nil {
		t.Fatalf("run audit failed: %v", err)
	}
	if result.Verdict.Pass {
		t.Fatalf("expected audit to fail, got %+v", result)
	}
	if result.Requests == nil || result.Requests.SucceededWithoutClaim != 1 || result.Requests.StaleInFlight != 1 {
		t.Fatalf("unexpected request audit: %+v", result.Requests)
	}
}

func mustNewAuditRequestStore(t *testing.T, redisClient *appredis.Client, prefix string) *claimrequest.RedisRequestStore {
	t.Helper()
	store, err := claimrequest.NewRedisRequestStore(redisClient, claimrequest.RequestStoreConfig{
		Prefix: prefix,
		TTL:    time.Hour,
	})
	if err != nil {
		t.Fatalf("new redis request store failed: %v", err)
	}
	return store
}

func mustCreateAndUpdateRequest(t *testing.T, ctx context.Context, store *claimrequest.RedisRequestStore, req claimrequest.Request, status claimrequest.Status, claimID int64, failureCode string) {
	t.Helper()
	if err := store.Create(ctx, req); err != nil {
		t.Fatalf("create request %s failed: %v", req.ID, err)
	}
	if err := store.UpdateStatus(ctx, req.ID, status, claimID, failureCode); err != nil {
		t.Fatalf("update request %s failed: %v", req.ID, err)
	}
}

func insertClaim(t *testing.T, db *sql.DB, couponID, userID int64, idemKey string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO coupon_claims (coupon_id, user_id, status, idempotency_key)
VALUES (?, ?, 'CLAIMED', ?)
`, couponID, userID, idemKey); err != nil {
		t.Fatalf("insert claim failed: %v", err)
	}
}

func writeBenchmarkReport(t *testing.T, report reportEnvelope) string {
	t.Helper()
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report failed: %v", err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write report failed: %v", err)
	}
	return path
}
