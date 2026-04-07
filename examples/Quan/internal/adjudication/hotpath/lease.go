package hotpath

import (
	"context"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type ReservationLease struct {
	ReservationID string
	CouponID      int64
	UserID        int64
	IdemKey       string
	State         string
	CreatedAt     time.Time
	LeaseUntil    time.Time
}

func (a *Adjudicator) Finalize(ctx context.Context, couponID, userID int64, idemKey, reservationID string, claimID int64) error {
	if a == nil || a.rdb == nil {
		return nil
	}
	successValue := fmt.Sprintf("SUCCESS:%d:%s", claimID, reservationID)
	_, err := a.rdb.Eval(ctx, `
local state = redis.call('HGET', KEYS[2], 'state')
local current = redis.call('GET', KEYS[1])
if state == false and current == ARGV[6] then
  return 1
end
if state ~= 'LEASED' and state ~= 'FINALIZED' then
  return 0
end
if current and current ~= 'PENDING:' .. ARGV[1] and current ~= ARGV[6] then
  return 0
end
if current == false or current == 'PENDING:' .. ARGV[1] then
  redis.call('SET', KEYS[1], ARGV[6], 'PX', ARGV[3])
end
redis.call('HSET', KEYS[2], 'state', 'FINALIZED', 'claim_id', ARGV[2], 'lease_until_ms', ARGV[4])
redis.call('PEXPIRE', KEYS[2], ARGV[5])
redis.call('ZREM', KEYS[3], ARGV[1])
redis.call('PUBLISH', KEYS[4], ARGV[6])
return 1
`, []string{
		IdemDecisionKey(couponID, userID, idemKey),
		ReservationLeaseKey(reservationID),
		ReservationLeaseIndexKey(),
		IdemDecisionResultChannel(couponID, userID, idemKey),
	}, reservationID,
		strconv.FormatInt(claimID, 10),
		a.successTTL.Milliseconds(),
		time.Now().UTC().Add(a.successTTL).UnixMilli(),
		a.leaseStateTTL.Milliseconds(),
		successValue,
	).Result()
	return err
}

func (a *Adjudicator) Rollback(ctx context.Context, couponID, userID int64, idemKey, reservationID string) error {
	if a == nil || a.rdb == nil {
		return nil
	}
	_, err := a.rdb.Eval(ctx, `
local state = redis.call('HGET', KEYS[4], 'state')
if state ~= 'LEASED' then
  return 0
end
local current = redis.call('GET', KEYS[3])
if current and current ~= 'PENDING:' .. ARGV[1] then
  return 0
end
if current == 'PENDING:' .. ARGV[1] then
  redis.call('DEL', KEYS[3])
end
redis.call('INCR', KEYS[1])
local count = redis.call('HINCRBY', KEYS[2], ARGV[2], -1)
if tonumber(count) <= 0 then
  redis.call('HDEL', KEYS[2], ARGV[2])
end
redis.call('HSET', KEYS[4], 'state', 'ROLLED_BACK', 'lease_until_ms', ARGV[3])
redis.call('PEXPIRE', KEYS[4], ARGV[4])
redis.call('ZREM', KEYS[5], ARGV[1])
redis.call('PUBLISH', KEYS[6], 'ROLLED_BACK')
return 1
`, []string{
		CampaignStockKey(couponID),
		CampaignUserCountKey(couponID),
		IdemDecisionKey(couponID, userID, idemKey),
		ReservationLeaseKey(reservationID),
		ReservationLeaseIndexKey(),
		IdemDecisionResultChannel(couponID, userID, idemKey),
	}, reservationID,
		strconv.FormatInt(userID, 10),
		time.Now().UTC().UnixMilli(),
		a.leaseStateTTL.Milliseconds(),
	).Result()
	return err
}

func (a *Adjudicator) ListExpiredReservations(ctx context.Context, now time.Time, limit int) ([]ReservationLease, error) {
	if a == nil || a.rdb == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	ids, err := a.rdb.ZRangeByScore(ctx, ReservationLeaseIndexKey(), &goredis.ZRangeBy{
		Min:   "-inf",
		Max:   strconv.FormatInt(now.UTC().UnixMilli(), 10),
		Count: int64(limit),
	}).Result()
	if err != nil {
		return nil, err
	}
	out := make([]ReservationLease, 0, len(ids))
	for _, reservationID := range ids {
		fields, leaseErr := a.rdb.HGetAll(ctx, ReservationLeaseKey(reservationID)).Result()
		if leaseErr != nil {
			return nil, leaseErr
		}
		if len(fields) == 0 {
			if remErr := a.rdb.ZRem(ctx, ReservationLeaseIndexKey(), reservationID).Err(); remErr != nil {
				return nil, remErr
			}
			continue
		}
		lease, parseErr := parseReservationLease(fields)
		if parseErr != nil {
			return nil, fmt.Errorf("parse reservation lease %s: %w", reservationID, parseErr)
		}
		if lease.State != "LEASED" {
			if remErr := a.rdb.ZRem(ctx, ReservationLeaseIndexKey(), reservationID).Err(); remErr != nil {
				return nil, remErr
			}
			continue
		}
		out = append(out, lease)
	}
	return out, nil
}

func parseReservationLease(fields map[string]string) (ReservationLease, error) {
	var out ReservationLease
	out.ReservationID = fields["reservation_id"]
	out.IdemKey = fields["idem_key"]
	out.State = fields["state"]

	couponID, err := strconv.ParseInt(fields["coupon_id"], 10, 64)
	if err != nil {
		return ReservationLease{}, fmt.Errorf("coupon_id: %w", err)
	}
	out.CouponID = couponID

	userID, err := strconv.ParseInt(fields["user_id"], 10, 64)
	if err != nil {
		return ReservationLease{}, fmt.Errorf("user_id: %w", err)
	}
	out.UserID = userID

	createdAtMs, err := strconv.ParseInt(fields["created_at_ms"], 10, 64)
	if err != nil {
		return ReservationLease{}, fmt.Errorf("created_at_ms: %w", err)
	}
	out.CreatedAt = time.UnixMilli(createdAtMs).UTC()

	leaseUntilMs, err := strconv.ParseInt(fields["lease_until_ms"], 10, 64)
	if err != nil {
		return ReservationLease{}, fmt.Errorf("lease_until_ms: %w", err)
	}
	out.LeaseUntil = time.UnixMilli(leaseUntilMs).UTC()
	return out, nil
}

func ReservationLeaseKey(reservationID string) string {
	return fmt.Sprintf("%s:reservation:%s", DecisionNamespace, reservationID)
}

func ReservationLeaseIndexKey() string {
	return fmt.Sprintf("%s:reservation:index", DecisionNamespace)
}
