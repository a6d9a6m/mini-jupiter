package coupon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"mini-jupiter/pkg/redis"

	goredis "github.com/redis/go-redis/v9"
)

const (
	decisionNamespace = "quan:claim"

	decisionCodeAdmitted     = "ADMITTED"
	decisionCodeIdemHit      = "IDEM_HIT"
	decisionCodePending      = "PENDING"
	decisionCodeAlready      = "ALREADY_CLAIMED"
	decisionCodeLimit        = "LIMIT_REACHED"
	decisionCodeSoldOut      = "SOLD_OUT"
	decisionCodeInactive     = "INACTIVE"
	decisionCodeCampaignMiss = "CAMPAIGN_MISS"
)

type decisionStatus string

const (
	decisionPending decisionStatus = "PENDING"
	decisionSuccess decisionStatus = "SUCCESS"
)

type claimDecision struct {
	Code          string
	ClaimID       int64
	ReservationID string
}

type Adjudicator struct {
	rdb         *goredis.Client
	pendingTTL  time.Duration
	successTTL  time.Duration
	pendingWait time.Duration
	pendingPoll time.Duration
}

func NewAdjudicator(client *redis.Client) *Adjudicator {
	if client == nil {
		return nil
	}
	return &Adjudicator{
		rdb:         client.Raw(),
		pendingTTL:  30 * time.Second,
		successTTL:  24 * time.Hour,
		pendingWait: 400 * time.Millisecond,
		pendingPoll: 10 * time.Millisecond,
	}
}

func (a *Adjudicator) Decide(ctx context.Context, campaign campaignSnapshot, userID int64, idemKey string, now time.Time, reservationID string) (claimDecision, error) {
	if a == nil || a.rdb == nil {
		return claimDecision{}, fmt.Errorf("coupon adjudicator redis client is nil")
	}
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
return {'ADMITTED', ARGV[3]}
`, []string{
		campaignStockKey(campaign.CouponID),
		campaignMetaKey(campaign.CouponID),
		campaignUserCountKey(campaign.CouponID),
		idemDecisionKey(campaign.CouponID, userID, idemKey),
	}, now.UTC().UnixMilli(), strconv.FormatInt(userID, 10), reservationID, a.pendingTTL.Milliseconds()).StringSlice()
	if err != nil {
		return claimDecision{}, err
	}
	return parseClaimDecision(result)
}

func (a *Adjudicator) EnsureCampaign(ctx context.Context, campaign campaignSnapshot) error {
	if a == nil || a.rdb == nil {
		return fmt.Errorf("coupon adjudicator redis client is nil")
	}
	pipe := a.rdb.TxPipeline()
	pipe.SetNX(ctx, campaignStockKey(campaign.CouponID), campaign.AvailableStock, 0)
	pipe.HSetNX(ctx, campaignMetaKey(campaign.CouponID), "status", campaign.Status)
	pipe.HSetNX(ctx, campaignMetaKey(campaign.CouponID), "start_ms", campaign.StartAt.UTC().UnixMilli())
	pipe.HSetNX(ctx, campaignMetaKey(campaign.CouponID), "end_ms", campaign.EndAt.UTC().UnixMilli())
	pipe.HSetNX(ctx, campaignMetaKey(campaign.CouponID), "per_user_limit", normalizePerUserLimit(campaign.PerUserLimit))
	_, err := pipe.Exec(ctx)
	return err
}

func (a *Adjudicator) Finalize(ctx context.Context, couponID, userID int64, idemKey, reservationID string, claimID int64) error {
	if a == nil || a.rdb == nil {
		return nil
	}
	_, err := a.rdb.Eval(ctx, `
local current = redis.call('GET', KEYS[1])
if current == 'PENDING:' .. ARGV[1] then
  redis.call('SET', KEYS[1], 'SUCCESS:' .. ARGV[2], 'PX', ARGV[3])
  return 1
end
if current == 'SUCCESS:' .. ARGV[2] then
  return 1
end
return 0
`, []string{idemDecisionKey(couponID, userID, idemKey)}, reservationID, strconv.FormatInt(claimID, 10), a.successTTL.Milliseconds()).Result()
	return err
}

func (a *Adjudicator) Rollback(ctx context.Context, couponID, userID int64, idemKey, reservationID string) error {
	if a == nil || a.rdb == nil {
		return nil
	}
	_, err := a.rdb.Eval(ctx, `
local current = redis.call('GET', KEYS[3])
if current ~= 'PENDING:' .. ARGV[1] then
  return 0
end
redis.call('DEL', KEYS[3])
redis.call('INCR', KEYS[1])
local count = redis.call('HINCRBY', KEYS[2], ARGV[2], -1)
if tonumber(count) <= 0 then
  redis.call('HDEL', KEYS[2], ARGV[2])
end
return 1
`, []string{
		campaignStockKey(couponID),
		campaignUserCountKey(couponID),
		idemDecisionKey(couponID, userID, idemKey),
	}, reservationID, strconv.FormatInt(userID, 10)).Result()
	return err
}

func (a *Adjudicator) WaitResult(ctx context.Context, couponID, userID int64, idemKey string) (int64, bool, error) {
	if a == nil || a.rdb == nil {
		return 0, false, nil
	}
	deadline := time.Now().Add(a.pendingWait)
	key := idemDecisionKey(couponID, userID, idemKey)
	for time.Now().Before(deadline) {
		val, err := a.rdb.Get(ctx, key).Result()
		if err != nil {
			if errors.Is(err, goredis.Nil) {
				return 0, false, nil
			}
			return 0, false, err
		}
		if strings.HasPrefix(val, "SUCCESS:") {
			claimID, convErr := strconv.ParseInt(strings.TrimPrefix(val, "SUCCESS:"), 10, 64)
			if convErr != nil {
				return 0, false, fmt.Errorf("invalid success claim id: %w", convErr)
			}
			return claimID, true, nil
		}
		if !strings.HasPrefix(val, "PENDING:") {
			return 0, false, nil
		}
		time.Sleep(a.pendingPoll)
	}
	return 0, false, nil
}

func normalizePerUserLimit(limit int) int {
	if limit <= 0 {
		return 1
	}
	return limit
}

func parseClaimDecision(result []string) (claimDecision, error) {
	if len(result) == 0 {
		return claimDecision{}, fmt.Errorf("empty redis decision result")
	}
	out := claimDecision{Code: result[0]}
	if len(result) > 1 {
		switch out.Code {
		case decisionCodeIdemHit:
			claimID, err := strconv.ParseInt(result[1], 10, 64)
			if err != nil {
				return claimDecision{}, fmt.Errorf("parse redis decision claim id: %w", err)
			}
			out.ClaimID = claimID
		case decisionCodeAdmitted, decisionCodePending:
			out.ReservationID = result[1]
		}
	}
	return out, nil
}

func campaignMetaKey(couponID int64) string {
	return fmt.Sprintf("%s:campaign:%d:meta", decisionNamespace, couponID)
}

func campaignStockKey(couponID int64) string {
	return fmt.Sprintf("%s:campaign:%d:stock", decisionNamespace, couponID)
}

func campaignUserCountKey(couponID int64) string {
	return fmt.Sprintf("%s:campaign:%d:user_count", decisionNamespace, couponID)
}

func idemDecisionKey(couponID, userID int64, idemKey string) string {
	return fmt.Sprintf("%s:coupon:%d:user:%d:idem:%s", decisionNamespace, couponID, userID, idemKey)
}
