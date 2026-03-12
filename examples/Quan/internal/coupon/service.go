package coupon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	apperr "mini-jupiter/pkg/errors"
	"mini-jupiter/pkg/redis"

	goredis "github.com/redis/go-redis/v9"
)

type Service struct {
	repo    *Repository
	cache   *redis.Client
	idemTTL time.Duration
}

func NewService(repo *Repository, cache *redis.Client, idemTTL time.Duration) *Service {
	if idemTTL <= 0 {
		idemTTL = 24 * time.Hour
	}
	return &Service{repo: repo, cache: cache, idemTTL: idemTTL}
}

func (s *Service) Claim(ctx context.Context, couponID, userID int64, idemKey string) (ClaimRecord, error) {
	if idemKey != "" {
		rec, ok, err := s.getClaimFromIdemCache(ctx, couponID, userID, idemKey)
		if err != nil {
			return ClaimRecord{}, apperr.Wrap(apperr.CodeInternalError, "load idempotency cache failed", err)
		}
		if ok {
			return rec, nil
		}
	}

	rec, err := s.repo.ClaimCoupon(ctx, couponID, userID, idemKey)
	if err == nil {
		if idemKey != "" {
			_ = s.setIdemCache(ctx, couponID, userID, idemKey, rec.ID)
		}
		return rec, nil
	}

	switch {
	case errors.Is(err, ErrAlreadyClaimed):
		if idemKey != "" {
			prev, qErr := s.repo.FindClaimByUser(ctx, couponID, userID)
			if qErr == nil && prev.IdempotencyKey == idemKey {
				_ = s.setIdemCache(ctx, couponID, userID, idemKey, prev.ID)
				return prev, nil
			}
			if qErr != nil && !errors.Is(qErr, sql.ErrNoRows) {
				return ClaimRecord{}, apperr.Wrap(apperr.CodeInternalError, "query previous claim failed", qErr)
			}
		}
		return ClaimRecord{}, apperr.New(apperr.CodeConflict, "already claimed")
	case errors.Is(err, ErrClaimLimitReached):
		return ClaimRecord{}, apperr.New(apperr.CodeConflict, "claim limit reached")
	case errors.Is(err, ErrSoldOut):
		return ClaimRecord{}, apperr.New(apperr.CodeConflict, "coupon sold out")
	case errors.Is(err, ErrCampaignInactive):
		return ClaimRecord{}, apperr.New(apperr.CodeBadRequest, "campaign not active")
	case errors.Is(err, ErrCampaignNotFound):
		return ClaimRecord{}, apperr.New(apperr.CodeNotFound, "campaign not found")
	default:
		return ClaimRecord{}, apperr.Wrap(apperr.CodeInternalError, "claim coupon failed", err)
	}
}

func (s *Service) GetMyClaim(ctx context.Context, couponID, userID int64) (ClaimRecord, error) {
	rec, err := s.repo.FindClaimByUser(ctx, couponID, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClaimRecord{}, apperr.New(apperr.CodeNotFound, "claim not found")
		}
		return ClaimRecord{}, apperr.Wrap(apperr.CodeInternalError, "query claim failed", err)
	}
	return rec, nil
}

func (s *Service) getClaimFromIdemCache(ctx context.Context, couponID, userID int64, idemKey string) (ClaimRecord, bool, error) {
	if s.cache == nil {
		return ClaimRecord{}, false, nil
	}
	key := idemCacheKey(couponID, userID, idemKey)
	val, err := s.cache.Raw().Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return ClaimRecord{}, false, nil
		}
		return ClaimRecord{}, false, err
	}
	claimID, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return ClaimRecord{}, false, fmt.Errorf("invalid idempotency cache value: %w", err)
	}
	rec, err := s.repo.FindClaimByID(ctx, claimID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_ = s.cache.Raw().Del(ctx, key).Err()
			return ClaimRecord{}, false, nil
		}
		return ClaimRecord{}, false, err
	}
	return rec, true, nil
}

func (s *Service) setIdemCache(ctx context.Context, couponID, userID int64, idemKey string, claimID int64) error {
	if s.cache == nil {
		return nil
	}
	return s.cache.Raw().Set(ctx, idemCacheKey(couponID, userID, idemKey), strconv.FormatInt(claimID, 10), s.idemTTL).Err()
}

func idemCacheKey(couponID, userID int64, idemKey string) string {
	return fmt.Sprintf("quan:claim:idem:%d:%d:%s", couponID, userID, idemKey)
}
