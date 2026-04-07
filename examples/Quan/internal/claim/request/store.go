package request

import (
	"context"
	"fmt"
	"strconv"
	"time"

	appredis "mini-jupiter/pkg/redis"

	goredis "github.com/redis/go-redis/v9"
)

type RequestStoreConfig struct {
	Prefix             string
	TTL                time.Duration
	WaitReplicas       int
	WaitTimeout        time.Duration
	SkipWaitOnStatuses []Status
}

func (c RequestStoreConfig) withDefaults() RequestStoreConfig {
	if c.Prefix == "" {
		c.Prefix = "quan:claim"
	}
	if c.TTL <= 0 {
		c.TTL = 24 * time.Hour
	}
	if c.WaitTimeout <= 0 {
		c.WaitTimeout = 200 * time.Millisecond
	}
	return c
}

type RedisRequestStore struct {
	client           *appredis.Client
	raw              *goredis.Client
	cfg              RequestStoreConfig
	skipWaitStatuses map[Status]struct{}
}

func NewRedisRequestStore(client *appredis.Client, cfg RequestStoreConfig) (*RedisRequestStore, error) {
	if client == nil || client.Raw() == nil {
		return nil, fmt.Errorf("redis request store client is nil")
	}
	cfg = cfg.withDefaults()
	store := &RedisRequestStore{
		client:           client,
		raw:              client.Raw(),
		cfg:              cfg,
		skipWaitStatuses: make(map[Status]struct{}, len(cfg.SkipWaitOnStatuses)),
	}
	for _, status := range cfg.SkipWaitOnStatuses {
		store.skipWaitStatuses[status] = struct{}{}
	}
	return store, nil
}

func (s *RedisRequestStore) Create(ctx context.Context, req Request) error {
	if req.ID == "" {
		return fmt.Errorf("request id is empty")
	}
	key := s.requestKey(req.ID)
	idemKey := s.idemKey(req.CouponID, req.UserID, req.IdempotencyKey)
	nowMs := time.Now().UTC().UnixMilli()

	if idemKey != "" {
		ok, err := s.raw.SetNX(ctx, idemKey, req.ID, s.cfg.TTL).Result()
		if err != nil {
			return err
		}
		if !ok {
			return ErrRequestExists
		}
	}

	pipe := s.raw.TxPipeline()
	pipe.HSet(ctx, key, requestFields(req, nowMs))
	pipe.PExpire(ctx, key, s.cfg.TTL)
	pipe.ZAdd(ctx, s.statusKey(req.Status), goredis.Z{
		Score:  float64(nowMs),
		Member: req.ID,
	})
	if _, err := pipe.Exec(ctx); err != nil {
		if idemKey != "" {
			_ = s.raw.Del(ctx, idemKey).Err()
		}
		return err
	}
	return s.wait(ctx, req.Status)
}

func (s *RedisRequestStore) UpdateStatus(ctx context.Context, requestID string, status Status, claimID int64, failureCode string) error {
	key := s.requestKey(requestID)
	oldStatus, err := s.raw.HGet(ctx, key, "status").Result()
	if err != nil {
		if err == goredis.Nil {
			return ErrRequestNotFound
		}
		return err
	}
	if isTerminalStatus(Status(oldStatus)) && !isTerminalStatus(status) {
		return nil
	}
	nowMs := time.Now().UTC().UnixMilli()

	updates := map[string]any{
		"status":        string(status),
		"failure_code":  failureCode,
		"updated_at_ms": nowMs,
	}
	if claimID > 0 {
		updates["claim_id"] = claimID
	}
	if status == StatusSucceeded || status == StatusRolledBack || status == StatusFailed {
		updates["finished_at_ms"] = nowMs
	}
	if status == StatusProcessing {
		updates["processed_at_ms"] = nowMs
	}

	pipe := s.raw.TxPipeline()
	pipe.HSet(ctx, key, updates)
	pipe.PExpire(ctx, key, s.cfg.TTL)
	if oldStatus != "" && oldStatus != string(status) {
		pipe.ZRem(ctx, s.statusKey(Status(oldStatus)), requestID)
	}
	pipe.ZAdd(ctx, s.statusKey(status), goredis.Z{
		Score:  float64(nowMs),
		Member: requestID,
	})
	_, err = pipe.Exec(ctx)
	if err != nil {
		return err
	}
	return s.wait(ctx, status)
}

func (s *RedisRequestStore) Get(ctx context.Context, requestID string) (Request, bool, error) {
	fields, err := s.raw.HGetAll(ctx, s.requestKey(requestID)).Result()
	if err != nil {
		return Request{}, false, err
	}
	if len(fields) == 0 {
		return Request{}, false, nil
	}
	req, err := parseRequest(fields)
	if err != nil {
		return Request{}, false, err
	}
	return req, true, nil
}

func (s *RedisRequestStore) FindByIdempotency(ctx context.Context, couponID, userID int64, idemKey string) (Request, bool, error) {
	if idemKey == "" {
		return Request{}, false, nil
	}
	requestID, err := s.raw.Get(ctx, s.idemKey(couponID, userID, idemKey)).Result()
	if err != nil {
		if err == goredis.Nil {
			return Request{}, false, nil
		}
		return Request{}, false, err
	}
	return s.Get(ctx, requestID)
}

func (s *RedisRequestStore) ListByStatuses(ctx context.Context, statuses []Status, limit int) ([]Request, error) {
	if limit <= 0 {
		limit = 100
	}
	seen := make(map[string]struct{}, limit)
	out := make([]Request, 0, limit)
	for _, status := range statuses {
		ids, err := s.raw.ZRange(ctx, s.statusKey(status), 0, int64(limit-1)).Result()
		if err != nil {
			return nil, err
		}
		for _, requestID := range ids {
			if _, ok := seen[requestID]; ok {
				continue
			}
			req, found, err := s.Get(ctx, requestID)
			if err != nil {
				return nil, err
			}
			if !found {
				_ = s.raw.ZRem(ctx, s.statusKey(status), requestID).Err()
				continue
			}
			seen[requestID] = struct{}{}
			out = append(out, req)
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func (s *RedisRequestStore) requestKey(requestID string) string {
	return fmt.Sprintf("%s:request:%s", s.cfg.Prefix, requestID)
}

func (s *RedisRequestStore) statusKey(status Status) string {
	return fmt.Sprintf("%s:request:status:%s", s.cfg.Prefix, status)
}

func (s *RedisRequestStore) idemKey(couponID, userID int64, idemKey string) string {
	if idemKey == "" {
		return ""
	}
	return fmt.Sprintf("%s:request:idem:%d:%d:%s", s.cfg.Prefix, couponID, userID, idemKey)
}

func (s *RedisRequestStore) wait(ctx context.Context, status Status) error {
	if s.cfg.WaitReplicas <= 0 {
		return nil
	}
	if _, skip := s.skipWaitStatuses[status]; skip {
		return nil
	}
	_, err := s.client.Wait(ctx, s.cfg.WaitReplicas, s.cfg.WaitTimeout)
	return err
}

func requestFields(req Request, nowMs int64) map[string]any {
	return map[string]any{
		"request_id":      req.ID,
		"coupon_id":       req.CouponID,
		"user_id":         req.UserID,
		"idempotency_key": req.IdempotencyKey,
		"reservation_id":  req.ReservationID,
		"status":          string(req.Status),
		"claim_id":        req.ClaimID,
		"failure_code":    req.FailureCode,
		"accepted_at_ms":  nowMs,
		"processed_at_ms": 0,
		"finished_at_ms":  0,
		"updated_at_ms":   nowMs,
	}
}

func parseRequest(fields map[string]string) (Request, error) {
	req := Request{
		ID:             fields["request_id"],
		IdempotencyKey: fields["idempotency_key"],
		ReservationID:  fields["reservation_id"],
		Status:         Status(fields["status"]),
		FailureCode:    fields["failure_code"],
	}
	var err error
	if req.CouponID, err = parseInt64Field(fields, "coupon_id"); err != nil {
		return Request{}, err
	}
	if req.UserID, err = parseInt64Field(fields, "user_id"); err != nil {
		return Request{}, err
	}
	if fields["claim_id"] != "" {
		if req.ClaimID, err = parseInt64Field(fields, "claim_id"); err != nil {
			return Request{}, err
		}
	}
	if req.AcceptedAt, err = parseUnixMilliField(fields, "accepted_at_ms"); err != nil {
		return Request{}, err
	}
	if req.ProcessedAt, err = parseOptionalUnixMilliField(fields, "processed_at_ms"); err != nil {
		return Request{}, err
	}
	if req.FinishedAt, err = parseOptionalUnixMilliField(fields, "finished_at_ms"); err != nil {
		return Request{}, err
	}
	if req.UpdatedAt, err = parseUnixMilliField(fields, "updated_at_ms"); err != nil {
		return Request{}, err
	}
	return req, nil
}

func parseInt64Field(fields map[string]string, key string) (int64, error) {
	v, err := strconv.ParseInt(fields[key], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return v, nil
}

func parseUnixMilliField(fields map[string]string, key string) (time.Time, error) {
	v, err := parseInt64Field(fields, key)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(v).UTC(), nil
}

func parseOptionalUnixMilliField(fields map[string]string, key string) (time.Time, error) {
	if fields[key] == "" || fields[key] == "0" {
		return time.Time{}, nil
	}
	return parseUnixMilliField(fields, key)
}

func isTerminalStatus(status Status) bool {
	return status == StatusSucceeded || status == StatusRolledBack || status == StatusFailed
}
