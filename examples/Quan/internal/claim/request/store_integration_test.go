package request

import (
	"context"
	"errors"
	"testing"
	"time"

	"mini-jupiter/examples/Quan/internal/testutil/quanenv"
	appredis "mini-jupiter/pkg/redis"

	goredis "github.com/redis/go-redis/v9"
)

func TestRedisRequestStore_CreateGetAndFindByIdempotency(t *testing.T) {
	client := quanenv.OpenIntegrationRedis(t, 11)
	store := newIntegrationRequestStore(t, client, "store-create-get")
	ctx := context.Background()

	want := Request{
		ID:             "req-store-001",
		CouponID:       1101,
		UserID:         2101,
		IdempotencyKey: "idem-store-001",
		ReservationID:  "res-store-001",
		Status:         StatusAccepted,
	}
	if err := store.Create(ctx, want); err != nil {
		t.Fatalf("create request failed: %v", err)
	}

	got, found, err := store.Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("get request failed: %v", err)
	}
	if !found {
		t.Fatal("expected request to be found")
	}
	if got.ID != want.ID || got.CouponID != want.CouponID || got.UserID != want.UserID || got.IdempotencyKey != want.IdempotencyKey || got.ReservationID != want.ReservationID || got.Status != want.Status {
		t.Fatalf("loaded request mismatch: got %+v want %+v", got, want)
	}

	byIdem, idemFound, err := store.FindByIdempotency(ctx, want.CouponID, want.UserID, want.IdempotencyKey)
	if err != nil {
		t.Fatalf("find by idempotency failed: %v", err)
	}
	if !idemFound || byIdem.ID != want.ID {
		t.Fatalf("expected idempotency lookup to find %s, got found=%v req=%+v", want.ID, idemFound, byIdem)
	}

	statusIDs, err := client.Raw().ZRange(ctx, store.statusKey(StatusAccepted), 0, -1).Result()
	if err != nil {
		t.Fatalf("load status index failed: %v", err)
	}
	if len(statusIDs) != 1 || statusIDs[0] != want.ID {
		t.Fatalf("unexpected accepted status ids: %+v", statusIDs)
	}
}

func TestRedisRequestStore_UpdateStatusMovesIndexesAndProtectsTerminalState(t *testing.T) {
	client := quanenv.OpenIntegrationRedis(t, 12)
	store := newIntegrationRequestStore(t, client, "store-update-status")
	ctx := context.Background()

	req := Request{
		ID:             "req-store-002",
		CouponID:       1102,
		UserID:         2102,
		IdempotencyKey: "idem-store-002",
		ReservationID:  "res-store-002",
		Status:         StatusAccepted,
	}
	if err := store.Create(ctx, req); err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	if err := store.UpdateStatus(ctx, req.ID, StatusEnqueued, 0, ""); err != nil {
		t.Fatalf("mark enqueued failed: %v", err)
	}
	if err := store.UpdateStatus(ctx, req.ID, StatusSucceeded, 9102, ""); err != nil {
		t.Fatalf("mark succeeded failed: %v", err)
	}
	if err := store.UpdateStatus(ctx, req.ID, StatusAccepted, 0, "should-not-apply"); err != nil {
		t.Fatalf("terminal protection update returned error: %v", err)
	}

	got, found, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get request failed: %v", err)
	}
	if !found {
		t.Fatal("expected request to be found")
	}
	if got.Status != StatusSucceeded || got.ClaimID != 9102 || got.FailureCode != "" {
		t.Fatalf("expected terminal request to remain succeeded, got %+v", got)
	}

	enqueuedIDs, err := client.Raw().ZRange(ctx, store.statusKey(StatusEnqueued), 0, -1).Result()
	if err != nil {
		t.Fatalf("load enqueued ids failed: %v", err)
	}
	if len(enqueuedIDs) != 0 {
		t.Fatalf("expected enqueued index to be empty, got %+v", enqueuedIDs)
	}
	succeededIDs, err := client.Raw().ZRange(ctx, store.statusKey(StatusSucceeded), 0, -1).Result()
	if err != nil {
		t.Fatalf("load succeeded ids failed: %v", err)
	}
	if len(succeededIDs) != 1 || succeededIDs[0] != req.ID {
		t.Fatalf("unexpected succeeded ids: %+v", succeededIDs)
	}
}

func TestRedisRequestStore_ListByStatusesRemovesDanglingMembersAndDeduplicates(t *testing.T) {
	client := quanenv.OpenIntegrationRedis(t, 13)
	store := newIntegrationRequestStore(t, client, "store-list-statuses")
	ctx := context.Background()

	req := Request{
		ID:             "req-store-003",
		CouponID:       1103,
		UserID:         2103,
		IdempotencyKey: "idem-store-003",
		ReservationID:  "res-store-003",
		Status:         StatusAccepted,
	}
	if err := store.Create(ctx, req); err != nil {
		t.Fatalf("create request failed: %v", err)
	}
	if err := store.UpdateStatus(ctx, req.ID, StatusEnqueued, 0, ""); err != nil {
		t.Fatalf("mark enqueued failed: %v", err)
	}

	nowMs := float64(time.Now().UTC().UnixMilli())
	if err := client.Raw().ZAdd(ctx, store.statusKey(StatusProcessing), goredis.Z{Score: nowMs, Member: req.ID}).Err(); err != nil {
		t.Fatalf("inject duplicate status member failed: %v", err)
	}
	if err := client.Raw().ZAdd(ctx, store.statusKey(StatusAccepted), goredis.Z{Score: nowMs, Member: "missing-request"}).Err(); err != nil {
		t.Fatalf("inject dangling status member failed: %v", err)
	}

	listed, err := store.ListByStatuses(ctx, []Status{StatusAccepted, StatusEnqueued, StatusProcessing}, 10)
	if err != nil {
		t.Fatalf("list by statuses failed: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != req.ID || listed[0].Status != StatusEnqueued {
		t.Fatalf("expected single deduplicated enqueued request, got %+v", listed)
	}

	acceptedIDs, err := client.Raw().ZRange(ctx, store.statusKey(StatusAccepted), 0, -1).Result()
	if err != nil {
		t.Fatalf("load accepted ids failed: %v", err)
	}
	if len(acceptedIDs) != 0 {
		t.Fatalf("expected dangling accepted member to be cleaned up, got %+v", acceptedIDs)
	}
}

func TestRedisRequestStore_CreateRejectsDuplicateIdempotencyKey(t *testing.T) {
	client := quanenv.OpenIntegrationRedis(t, 14)
	store := newIntegrationRequestStore(t, client, "store-duplicate-idem")
	ctx := context.Background()

	first := Request{
		ID:             "req-store-004a",
		CouponID:       1104,
		UserID:         2104,
		IdempotencyKey: "idem-store-004",
		ReservationID:  "res-store-004a",
		Status:         StatusAccepted,
	}
	second := Request{
		ID:             "req-store-004b",
		CouponID:       first.CouponID,
		UserID:         first.UserID,
		IdempotencyKey: first.IdempotencyKey,
		ReservationID:  "res-store-004b",
		Status:         StatusAccepted,
	}
	if err := store.Create(ctx, first); err != nil {
		t.Fatalf("create first request failed: %v", err)
	}
	err := store.Create(ctx, second)
	if !errors.Is(err, ErrRequestExists) {
		t.Fatalf("expected ErrRequestExists, got %v", err)
	}

	got, found, getErr := store.FindByIdempotency(ctx, first.CouponID, first.UserID, first.IdempotencyKey)
	if getErr != nil {
		t.Fatalf("find by idempotency failed: %v", getErr)
	}
	if !found || got.ID != first.ID {
		t.Fatalf("expected original request to remain bound to idempotency key, got found=%v req=%+v", found, got)
	}
}

func newIntegrationRequestStore(t *testing.T, client *appredis.Client, suffix string) *RedisRequestStore {
	t.Helper()
	store, err := NewRedisRequestStore(client, RequestStoreConfig{
		Prefix: "itest:" + suffix + ":" + time.Now().UTC().Format("150405.000000000"),
		TTL:    time.Hour,
	})
	if err != nil {
		t.Fatalf("new redis request store failed: %v", err)
	}
	return store
}
