package hotpath

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	applog "mini-jupiter/pkg/log"
	"mini-jupiter/pkg/redis"

	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	DecisionNamespace = "quan:claim"

	DecisionCodeAdmitted     = "ADMITTED"
	DecisionCodeIdemHit      = "IDEM_HIT"
	DecisionCodePending      = "PENDING"
	DecisionCodeAlready      = "ALREADY_CLAIMED"
	DecisionCodeLimit        = "LIMIT_REACHED"
	DecisionCodeSoldOut      = "SOLD_OUT"
	DecisionCodeInactive     = "INACTIVE"
	DecisionCodeCampaignMiss = "CAMPAIGN_MISS"
)

type CampaignSnapshot struct {
	CouponID       int64
	Status         string
	AvailableStock int
	PerUserLimit   int
	StartAt        time.Time
	EndAt          time.Time
}

type decisionStatus string

const (
	decisionPending decisionStatus = "PENDING"
	decisionSuccess decisionStatus = "SUCCESS"
)

type ClaimDecision struct {
	Code          string
	ClaimID       int64
	ReservationID string
}

// Adjudicator 封装了领券热路径在 Redis 上的裁决逻辑。
// 它负责快速准入、预占状态管理、等待结果和热数据补齐，
// 但不负责最终的 MySQL 落账。
type Adjudicator struct {
	rdb           *goredis.Client
	pendingTTL    time.Duration
	successTTL    time.Duration
	leaseTTL      time.Duration
	leaseStateTTL time.Duration
	pendingWait   time.Duration
}

func NewAdjudicator(client *redis.Client) *Adjudicator {
	if client == nil {
		return nil
	}
	return &Adjudicator{
		rdb:           client.Raw(),
		pendingTTL:    30 * time.Second,
		successTTL:    24 * time.Hour,
		leaseTTL:      45 * time.Second,
		leaseStateTTL: 1 * time.Hour,
		pendingWait:   400 * time.Millisecond,
	}
}

func (a *Adjudicator) Raw() *goredis.Client {
	if a == nil {
		return nil
	}
	return a.rdb
}

func (a *Adjudicator) SetLeaseTTL(d time.Duration) {
	if a == nil {
		return
	}
	a.leaseTTL = d
}

func (a *Adjudicator) LeaseTTL() time.Duration {
	if a == nil {
		return 0
	}
	return a.leaseTTL
}

// Decide 是热路径裁决核心。
// 这里通过一段 Lua 在 Redis 内原子完成：
// 1. 活动热数据检查
// 2. 幂等状态检查
// 3. 库存和单用户领取次数检查
// 4. 预扣库存、预增用户计数、写入 PENDING 幂等状态和 reservation lease
func (a *Adjudicator) Decide(ctx context.Context, campaign CampaignSnapshot, userID int64, idemKey string, now time.Time, reservationID string) (ClaimDecision, error) {
	if a == nil || a.rdb == nil {
		return ClaimDecision{}, fmt.Errorf("coupon adjudicator redis client is nil")
	}
	// 这一段 Lua 是整个热裁决的原子边界：
	// 要么完整完成准入和预占，要么完全不生效。
	result, err := a.rdb.Eval(ctx, `
local stock = redis.call('GET', KEYS[1])
if not stock then
  return {'CAMPAIGN_MISS'}
end

local status = redis.call('HGET', KEYS[2], 'status')
local start_ms = tonumber(redis.call('HGET', KEYS[2], 'start_ms') or '0')
local end_ms = tonumber(redis.call('HGET', KEYS[2], 'end_ms') or '0')
local limit = tonumber(redis.call('HGET', KEYS[2], 'per_user_limit') or '1')
local now_ms = tonumber(ARGV[1])
local user_id = ARGV[2]
local idem_val = redis.call('GET', KEYS[4])

if idem_val then
  if string.sub(idem_val, 1, 8) == 'SUCCESS:' then
    return {'IDEM_HIT', string.sub(idem_val, 9)}
  end
  if string.sub(idem_val, 1, 8) == 'PENDING:' then
    return {'PENDING', string.sub(idem_val, 9)}
  end
end

if status ~= 'ACTIVE' or now_ms < start_ms or now_ms > end_ms then
  return {'INACTIVE'}
end

local user_count = tonumber(redis.call('HGET', KEYS[3], user_id) or '0')
if user_count >= limit then
  if limit == 1 then
    return {'ALREADY_CLAIMED'}
  end
  return {'LIMIT_REACHED'}
end

local remain = tonumber(stock or '0')
if remain <= 0 then
  return {'SOLD_OUT'}
end

remain = redis.call('DECR', KEYS[1])
if tonumber(remain) < 0 then
  redis.call('INCR', KEYS[1])
  return {'SOLD_OUT'}
end

local new_count = redis.call('HINCRBY', KEYS[3], user_id, 1)
if tonumber(new_count) > limit then
  redis.call('HINCRBY', KEYS[3], user_id, -1)
  redis.call('INCR', KEYS[1])
  if limit == 1 then
    return {'ALREADY_CLAIMED'}
  end
  return {'LIMIT_REACHED'}
end

redis.call('SET', KEYS[4], 'PENDING:' .. ARGV[3], 'PX', ARGV[4])
redis.call('HSET', KEYS[5],
  'reservation_id', ARGV[3],
  'coupon_id', ARGV[5],
  'user_id', ARGV[2],
  'idem_key', ARGV[6],
  'state', 'LEASED',
  'created_at_ms', ARGV[1],
  'lease_until_ms', ARGV[7]
)
redis.call('PEXPIRE', KEYS[5], ARGV[8])
redis.call('ZADD', KEYS[6], ARGV[7], ARGV[3])
return {'ADMITTED', ARGV[3]}
`, []string{
		CampaignStockKey(campaign.CouponID),
		CampaignMetaKey(campaign.CouponID),
		CampaignUserCountKey(campaign.CouponID),
		IdemDecisionKey(campaign.CouponID, userID, idemKey),
		ReservationLeaseKey(reservationID),
		ReservationLeaseIndexKey(),
	}, now.UTC().UnixMilli(),
		strconv.FormatInt(userID, 10),
		reservationID,
		a.pendingTTL.Milliseconds(),
		strconv.FormatInt(campaign.CouponID, 10),
		idemKey,
		now.UTC().Add(a.leaseTTL).UnixMilli(),
		a.leaseStateTTL.Milliseconds(),
	).StringSlice()
	if err != nil {
		return ClaimDecision{}, err
	}
	return parseClaimDecision(result)
}

// EnsureCampaign 在 Redis 热数据缺失或不完整时，把 MySQL 里的活动快照补齐进 Redis。
// 它不会重置已有热状态，只补缺失部分。
func (a *Adjudicator) EnsureCampaign(ctx context.Context, campaign CampaignSnapshot) error {
	if a == nil || a.rdb == nil {
		return fmt.Errorf("coupon adjudicator redis client is nil")
	}
	result, err := a.rdb.Eval(ctx, `
local stock_exists = redis.call('EXISTS', KEYS[1]) == 1
local meta = redis.call('HMGET', KEYS[2], 'status', 'start_ms', 'end_ms', 'per_user_limit')
local meta_complete = meta[1] ~= false and meta[1] ~= nil
  and meta[2] ~= false and meta[2] ~= nil
  and meta[3] ~= false and meta[3] ~= nil
  and meta[4] ~= false and meta[4] ~= nil

if not stock_exists then
  redis.call('SET', KEYS[1], ARGV[1])
end
if not meta_complete then
  redis.call('HSET', KEYS[2],
    'status', ARGV[2],
    'start_ms', ARGV[3],
    'end_ms', ARGV[4],
    'per_user_limit', ARGV[5]
  )
end

if not stock_exists and not meta_complete then
  return 'INITIALIZED'
end
if stock_exists and not meta_complete then
  return 'REPAIRED_META'
end
if not stock_exists and meta_complete then
  return 'REPAIRED_STOCK'
end
return 'UNCHANGED'
`, []string{
		CampaignStockKey(campaign.CouponID),
		CampaignMetaKey(campaign.CouponID),
	},
		campaign.AvailableStock,
		campaign.Status,
		campaign.StartAt.UTC().UnixMilli(),
		campaign.EndAt.UTC().UnixMilli(),
		normalizePerUserLimit(campaign.PerUserLimit),
	).Text()
	if err != nil {
		return err
	}
	switch result {
	case "REPAIRED_META", "REPAIRED_STOCK":
		applog.L(ctx).Warn("repaired incomplete redis campaign hot-path state",
			zap.Int64("coupon_id", campaign.CouponID),
			zap.String("repair", result),
		)
	}
	return nil
}

// WaitResult 用于处理相同幂等键命中的 PENDING 场景。
// 当前请求会先看幂等结果键，再短暂订阅结果 channel，等待前一个请求 finalize/rollback。
func (a *Adjudicator) WaitResult(ctx context.Context, couponID, userID int64, idemKey string) (int64, bool, error) {
	if a == nil || a.rdb == nil {
		return 0, false, nil
	}
	key := IdemDecisionKey(couponID, userID, idemKey)
	channel := IdemDecisionResultChannel(couponID, userID, idemKey)
	pubsub := a.rdb.Subscribe(ctx, channel)
	defer pubsub.Close()
	if _, err := pubsub.Receive(ctx); err != nil {
		return a.degradeWaitResult(ctx, couponID, userID, "subscribe for pending claim result failed", err)
	}

	val, err := a.rdb.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return 0, false, nil
		}
		return a.degradeWaitResult(ctx, couponID, userID, "load pending claim result state failed", err)
	}
	if claimID, ok, err := parseWaitResultValue(val); ok || err != nil {
		return claimID, ok, err
	}

	waitCtx, cancel := context.WithTimeout(ctx, a.pendingWait)
	defer cancel()
	msg, err := pubsub.ReceiveMessage(waitCtx)
	if err != nil {
		return a.degradeWaitResult(ctx, couponID, userID, "receive pending claim result message failed", err)
	}
	return parseWaitResultValue(msg.Payload)
}

// degradeWaitResult 把等待结果旁路上的异常降级成“稍后重试”，
// 避免因为订阅或瞬时网络问题把业务请求直接打成硬失败。
func (a *Adjudicator) degradeWaitResult(ctx context.Context, couponID, userID int64, msg string, err error) (int64, bool, error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return 0, false, nil
	}
	applog.L(ctx).Warn(msg,
		zap.Int64("coupon_id", couponID),
		zap.Int64("user_id", userID),
		zap.Error(err),
	)
	return 0, false, nil
}

func normalizePerUserLimit(limit int) int {
	if limit <= 0 {
		return 1
	}
	return limit
}

// parseClaimDecision 把 Redis Lua 返回的字符串数组转换成业务可消费的裁决结果。
func parseClaimDecision(result []string) (ClaimDecision, error) {
	if len(result) == 0 {
		return ClaimDecision{}, fmt.Errorf("empty redis decision result")
	}
	out := ClaimDecision{Code: result[0]}
	if len(result) > 1 {
		switch out.Code {
		case DecisionCodeIdemHit:
			claimID, err := strconv.ParseInt(result[1], 10, 64)
			if err != nil {
				return ClaimDecision{}, fmt.Errorf("parse redis decision claim id: %w", err)
			}
			out.ClaimID = claimID
		case DecisionCodeAdmitted, DecisionCodePending:
			out.ReservationID = result[1]
		}
	}
	return out, nil
}

func CampaignMetaKey(couponID int64) string {
	return fmt.Sprintf("%s:campaign:%d:meta", DecisionNamespace, couponID)
}

func CampaignStockKey(couponID int64) string {
	return fmt.Sprintf("%s:campaign:%d:stock", DecisionNamespace, couponID)
}

func CampaignUserCountKey(couponID int64) string {
	return fmt.Sprintf("%s:campaign:%d:user_count", DecisionNamespace, couponID)
}

func IdemDecisionKey(couponID, userID int64, idemKey string) string {
	return fmt.Sprintf("%s:coupon:%d:user:%d:idem:%s", DecisionNamespace, couponID, userID, idemKey)
}

func IdemDecisionResultChannel(couponID, userID int64, idemKey string) string {
	return fmt.Sprintf("%s:coupon:%d:user:%d:idem:%s:result", DecisionNamespace, couponID, userID, idemKey)
}

// parseWaitResultValue 只识别 finalize 后写入的 SUCCESS 状态。
// 对调用方来说，PENDING 或空值都表示“还没有最终结果”。
func parseWaitResultValue(val string) (int64, bool, error) {
	if strings.HasPrefix(val, "SUCCESS:") {
		claimID, convErr := strconv.ParseInt(strings.TrimPrefix(val, "SUCCESS:"), 10, 64)
		if convErr != nil {
			return 0, false, fmt.Errorf("invalid success claim id: %w", convErr)
		}
		return claimID, true, nil
	}
	if strings.HasPrefix(val, "PENDING:") {
		return 0, false, nil
	}
	return 0, false, nil
}
