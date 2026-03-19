package claimservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mini-jupiter/examples/Quan/internal/adjudication/hotpath"
	claimmodel "mini-jupiter/examples/Quan/internal/claim/model"

	goredis "github.com/redis/go-redis/v9"
)

func (s *Service) cacheClaim(ctx context.Context, rec claimmodel.Record) {
	if s == nil || s.adjudicator == nil || s.adjudicator.Raw() == nil || s.claimCacheTTL <= 0 {
		return
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_ = s.adjudicator.Raw().Set(ctx, ClaimCacheKey(rec.CouponID, rec.UserID), payload, s.claimCacheTTL).Err()
}

func (s *Service) loadCachedClaim(ctx context.Context, couponID, userID int64) (claimmodel.Record, bool, error) {
	if s == nil || s.adjudicator == nil || s.adjudicator.Raw() == nil {
		return claimmodel.Record{}, false, nil
	}
	raw, err := s.adjudicator.Raw().Get(ctx, ClaimCacheKey(couponID, userID)).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return claimmodel.Record{}, false, nil
		}
		return claimmodel.Record{}, false, err
	}
	var rec claimmodel.Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return claimmodel.Record{}, false, fmt.Errorf("unmarshal cached claim: %w", err)
	}
	return rec, true, nil
}

func ClaimCacheKey(couponID, userID int64) string {
	return fmt.Sprintf("%s:coupon:%d:user:%d:claim", hotpath.DecisionNamespace, couponID, userID)
}

func normalizeClaimCacheTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return 24 * time.Hour
	}
	return ttl
}
