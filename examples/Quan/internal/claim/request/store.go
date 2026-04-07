package request

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	ttlMs := s.cfg.TTL.Milliseconds()

	result, err := s.raw.Eval(ctx, `
local idem_key = KEYS[3]
if idem_key ~= '' then
  local existing = redis.call('GET', idem_key)
  if existing then
    return {'EXISTS', existing}
  end
end
redis.call('HSET', KEYS[1],
  'request_id', ARGV[1],
  'coupon_id', ARGV[2],
  'user_id', ARGV[3],
  'idempotency_key', ARGV[4],
  'reservation_id', ARGV[5],
  'status', ARGV[6],
  'claim_id', ARGV[7],
  'failure_code', ARGV[8],
  'accepted_at_ms', ARGV[9],
  'processed_at_ms', ARGV[10],
  'finished_at_ms', ARGV[11],
  'updated_at_ms', ARGV[12]
)
redis.call('PEXPIRE', KEYS[1], ARGV[13])
redis.call('ZADD', KEYS[2], ARGV[14], ARGV[1])
if idem_key ~= '' then
  redis.call('SET', idem_key, ARGV[1], 'PX', ARGV[13])
end
return {'CREATED', ARGV[1]}
`, []string{key, s.statusKey(req.Status), idemKey},
		req.ID,
		strconv.FormatInt(req.CouponID, 10),
		strconv.FormatInt(req.UserID, 10),
		req.IdempotencyKey,
		req.ReservationID,
		string(req.Status),
		strconv.FormatInt(req.ClaimID, 10),
		req.FailureCode,
		strconv.FormatInt(nowMs, 10),
		"0",
		"0",
		strconv.FormatInt(nowMs, 10),
		strconv.FormatInt(ttlMs, 10),
		strconv.FormatInt(nowMs, 10),
	).StringSlice()
	if err != nil {
		if recovered, recoveryErr := s.recoverCreateResult(ctx, req); recoveryErr == nil && recovered {
			return s.wait(ctx, req.ID, req.Status)
		}
		return err
	}
	if len(result) == 0 {
		return fmt.Errorf("empty create result")
	}
	if result[0] == "EXISTS" {
		return ErrRequestExists
	}
	return s.wait(ctx, req.ID, req.Status)
}

func (s *RedisRequestStore) UpdateStatus(ctx context.Context, requestID string, status Status, claimID int64, failureCode string) error {
	key := s.requestKey(requestID)
	nowMs := time.Now().UTC().UnixMilli()
	allowed := allowedPreviousStatuses(status)
	result, err := s.raw.Eval(ctx, `
local current = redis.call('HGET', KEYS[1], 'status')
if current == false or current == nil then
  return {'NOT_FOUND'}
end
local target = ARGV[1]
local allowed_csv = ARGV[2]
local allowed = {}
for value in string.gmatch(allowed_csv, '([^,]+)') do
  allowed[value] = true
end
if not allowed[current] then
  return {'SKIPPED', current}
end
redis.call('HSET', KEYS[1], 'status', target, 'failure_code', ARGV[3], 'updated_at_ms', ARGV[4])
if tonumber(ARGV[5]) > 0 then
  redis.call('HSET', KEYS[1], 'claim_id', ARGV[5])
end
if target == 'SUCCEEDED' or target == 'ROLLED_BACK' or target == 'FAILED' then
  redis.call('HSET', KEYS[1], 'finished_at_ms', ARGV[4])
end
if target == 'PROCESSING' then
  redis.call('HSET', KEYS[1], 'processed_at_ms', ARGV[4])
end
redis.call('PEXPIRE', KEYS[1], ARGV[6])
for i = 2, 8 do
  redis.call('ZREM', KEYS[i], ARGV[8])
end
redis.call('ZADD', KEYS[tonumber(ARGV[7])], ARGV[9], ARGV[8])
return {'UPDATED', current}
`, []string{
		key,
		s.statusKey(StatusAccepted),
		s.statusKey(StatusPublishing),
		s.statusKey(StatusEnqueued),
		s.statusKey(StatusProcessing),
		s.statusKey(StatusSucceeded),
		s.statusKey(StatusRolledBack),
		s.statusKey(StatusFailed),
	},
		string(status),
		strings.Join(statusStrings(allowed), ","),
		failureCode,
		strconv.FormatInt(nowMs, 10),
		strconv.FormatInt(claimID, 10),
		strconv.FormatInt(s.cfg.TTL.Milliseconds(), 10),
		strconv.Itoa(statusKeyIndex(status)),
		requestID,
		strconv.FormatInt(nowMs, 10),
	).StringSlice()
	if err != nil {
		return err
	}
	if len(result) == 0 {
		return fmt.Errorf("empty update status result")
	}
	switch result[0] {
	case "NOT_FOUND":
		return ErrRequestNotFound
	case "SKIPPED":
		current := Status("")
		if len(result) > 1 {
			current = Status(result[1])
		}
		return TransitionSkippedError{
			RequestID: requestID,
			Current:   current,
			Target:    status,
		}
	case "UPDATED":
	default:
		return fmt.Errorf("unknown update status result: %v", result)
	}
	return s.wait(ctx, requestID, status)
}

func (s *RedisRequestStore) CompareAndUpdateStatus(ctx context.Context, snapshot Request, status Status, claimID int64, failureCode string) (bool, error) {
	if snapshot.ID == "" {
		return false, fmt.Errorf("request id is empty")
	}
	key := s.requestKey(snapshot.ID)
	nowMs := time.Now().UTC().UnixMilli()
	allowed := allowedPreviousStatuses(status)
	result, err := s.raw.Eval(ctx, `
local current = redis.call('HGET', KEYS[1], 'status')
if current == false or current == nil then
  return {'NOT_FOUND'}
end
local updated_at = redis.call('HGET', KEYS[1], 'updated_at_ms')
if updated_at ~= ARGV[10] then
  return {'STALE', current, updated_at}
end
if current ~= ARGV[11] then
  return {'STALE', current, updated_at}
end
local target = ARGV[1]
local allowed_csv = ARGV[2]
local allowed = {}
for value in string.gmatch(allowed_csv, '([^,]+)') do
  allowed[value] = true
end
if not allowed[current] then
  return {'SKIPPED', current}
end
redis.call('HSET', KEYS[1], 'status', target, 'failure_code', ARGV[3], 'updated_at_ms', ARGV[4])
if tonumber(ARGV[5]) > 0 then
  redis.call('HSET', KEYS[1], 'claim_id', ARGV[5])
end
if target == 'SUCCEEDED' or target == 'ROLLED_BACK' or target == 'FAILED' then
  redis.call('HSET', KEYS[1], 'finished_at_ms', ARGV[4])
end
if target == 'PROCESSING' then
  redis.call('HSET', KEYS[1], 'processed_at_ms', ARGV[4])
end
redis.call('PEXPIRE', KEYS[1], ARGV[6])
for i = 2, 8 do
  redis.call('ZREM', KEYS[i], ARGV[8])
end
redis.call('ZADD', KEYS[tonumber(ARGV[7])], ARGV[9], ARGV[8])
return {'UPDATED', current}
`, []string{
		key,
		s.statusKey(StatusAccepted),
		s.statusKey(StatusPublishing),
		s.statusKey(StatusEnqueued),
		s.statusKey(StatusProcessing),
		s.statusKey(StatusSucceeded),
		s.statusKey(StatusRolledBack),
		s.statusKey(StatusFailed),
	},
		string(status),
		strings.Join(statusStrings(allowed), ","),
		failureCode,
		strconv.FormatInt(nowMs, 10),
		strconv.FormatInt(claimID, 10),
		strconv.FormatInt(s.cfg.TTL.Milliseconds(), 10),
		strconv.Itoa(statusKeyIndex(status)),
		snapshot.ID,
		strconv.FormatInt(nowMs, 10),
		strconv.FormatInt(snapshot.UpdatedAt.UTC().UnixMilli(), 10),
		string(snapshot.Status),
	).StringSlice()
	if err != nil {
		return false, err
	}
	if len(result) == 0 {
		return false, fmt.Errorf("empty compare-and-update result")
	}
	switch result[0] {
	case "NOT_FOUND":
		return false, ErrRequestNotFound
	case "STALE", "SKIPPED":
		return false, nil
	case "UPDATED":
		return true, s.wait(ctx, snapshot.ID, status)
	default:
		return false, fmt.Errorf("unknown compare-and-update result: %v", result)
	}
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

func (s *RedisRequestStore) recoverCreateResult(ctx context.Context, req Request) (bool, error) {
	existing, found, err := s.Get(ctx, req.ID)
	if err == nil && found {
		return existing.CouponID == req.CouponID && existing.UserID == req.UserID && existing.IdempotencyKey == req.IdempotencyKey, nil
	}
	if err != nil && !errors.Is(err, ErrRequestNotFound) {
		return false, err
	}
	if req.IdempotencyKey == "" {
		return false, nil
	}
	byIdem, found, err := s.FindByIdempotency(ctx, req.CouponID, req.UserID, req.IdempotencyKey)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	return byIdem.ID == req.ID, nil
}

func (s *RedisRequestStore) wait(ctx context.Context, requestID string, status Status) error {
	if s.cfg.WaitReplicas <= 0 {
		return nil
	}
	if _, skip := s.skipWaitStatuses[status]; skip {
		return nil
	}
	acknowledged, err := s.client.Wait(ctx, s.cfg.WaitReplicas, s.cfg.WaitTimeout)
	if err == nil && acknowledged >= int64(s.cfg.WaitReplicas) {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("replica confirmation incomplete: got %d want %d", acknowledged, s.cfg.WaitReplicas)
	}
	return DurabilityPendingError{
		RequestID: requestID,
		Status:    status,
		Err:       err,
	}
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

func allowedPreviousStatuses(target Status) []Status {
	switch target {
	case StatusAccepted:
		return []Status{StatusAccepted}
	case StatusPublishing:
		return []Status{StatusAccepted, StatusPublishing}
	case StatusEnqueued:
		return []Status{StatusAccepted, StatusPublishing, StatusEnqueued}
	case StatusProcessing:
		return []Status{StatusEnqueued, StatusProcessing}
	case StatusSucceeded:
		return []Status{StatusProcessing, StatusSucceeded}
	case StatusRolledBack:
		return []Status{StatusProcessing, StatusRolledBack}
	case StatusFailed:
		return []Status{StatusProcessing, StatusFailed}
	default:
		return []Status{target}
	}
}

func statusStrings(statuses []Status) []string {
	out := make([]string, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, string(status))
	}
	return out
}

func statusKeyIndex(status Status) int {
	switch status {
	case StatusAccepted:
		return 2
	case StatusPublishing:
		return 3
	case StatusEnqueued:
		return 4
	case StatusProcessing:
		return 5
	case StatusSucceeded:
		return 6
	case StatusRolledBack:
		return 7
	case StatusFailed:
		return 8
	default:
		return 2
	}
}
