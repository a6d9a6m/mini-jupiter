package claimservice

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"mini-jupiter/examples/Quan/internal/adjudication/hotpath"
	claimmodel "mini-jupiter/examples/Quan/internal/claim/model"
	claimrepo "mini-jupiter/examples/Quan/internal/claim/repository"
	apperr "mini-jupiter/pkg/errors"
	applog "mini-jupiter/pkg/log"
	"mini-jupiter/pkg/redis"

	"go.uber.org/zap"
)

type Service struct {
	repo          *claimrepo.Repository
	adjudicator   *hotpath.Adjudicator
	claimCacheTTL time.Duration
}

var reservationIDFallback atomic.Uint64

func NewService(repo *claimrepo.Repository, cache *redis.Client, idemTTL time.Duration) *Service {
	return NewServiceWithAdjudicator(repo, hotpath.NewAdjudicator(cache), idemTTL)
}

func NewServiceWithAdjudicator(repo *claimrepo.Repository, adjudicator *hotpath.Adjudicator, idemTTL time.Duration) *Service {
	return &Service{
		repo:          repo,
		adjudicator:   adjudicator,
		claimCacheTTL: normalizeClaimCacheTTL(idemTTL),
	}
}

func (s *Service) Claim(ctx context.Context, couponID, userID int64, idemKey string) (claimmodel.Record, error) {
	if s.adjudicator == nil {
		return s.claimWithoutRedis(ctx, couponID, userID, idemKey)
	}

	for attempt := 0; attempt < 2; attempt++ {
		rec, done, err := s.claimWithRedis(ctx, couponID, userID, idemKey)
		if done {
			return rec, err
		}
		if err != nil {
			return claimmodel.Record{}, err
		}
	}
	return claimmodel.Record{}, apperr.New(apperr.CodeTooManyRequests, "request is still being processed")
}

func (s *Service) claimWithoutRedis(ctx context.Context, couponID, userID int64, idemKey string) (claimmodel.Record, error) {
	rec, err := s.repo.ClaimCoupon(ctx, couponID, userID, idemKey)
	if err == nil {
		s.cacheClaim(ctx, rec)
		return rec, nil
	}
	return claimmodel.Record{}, s.mapClaimErr(err)
}

func (s *Service) claimWithRedis(ctx context.Context, couponID, userID int64, idemKey string) (claimmodel.Record, bool, error) {
	reservationID := ClaimReservationID()
	decision, err := s.adjudicator.Decide(ctx, hotpath.CampaignSnapshot{CouponID: couponID}, userID, idemKey, time.Now().UTC(), reservationID)
	if err != nil {
		return claimmodel.Record{}, true, apperr.Wrap(apperr.CodeInternalError, "redis adjudication failed", err)
	}

	switch decision.Code {
	case hotpath.DecisionCodeCampaignMiss:
		campaign, err := s.repo.LoadCampaign(ctx, couponID)
		if err != nil {
			return claimmodel.Record{}, true, s.mapClaimErr(err)
		}
		if err := s.adjudicator.EnsureCampaign(ctx, campaign); err != nil {
			return claimmodel.Record{}, true, apperr.Wrap(apperr.CodeInternalError, "hydrate redis campaign failed", err)
		}
		return claimmodel.Record{}, false, nil
	case hotpath.DecisionCodeIdemHit:
		rec, err := s.repo.FindClaimByID(ctx, decision.ClaimID)
		if err != nil {
			return claimmodel.Record{}, true, apperr.Wrap(apperr.CodeInternalError, "query replay claim failed", err)
		}
		s.cacheClaim(ctx, rec)
		return rec, true, nil
	case hotpath.DecisionCodePending:
		rec, ok, err := s.waitPendingClaim(ctx, couponID, userID, idemKey)
		if err != nil {
			return claimmodel.Record{}, true, err
		}
		if ok {
			return rec, true, nil
		}
		return claimmodel.Record{}, false, nil
	case hotpath.DecisionCodeAlready:
		return claimmodel.Record{}, true, apperr.New(apperr.CodeConflict, "already claimed")
	case hotpath.DecisionCodeLimit:
		return claimmodel.Record{}, true, apperr.New(apperr.CodeConflict, "claim limit reached")
	case hotpath.DecisionCodeSoldOut:
		return claimmodel.Record{}, true, apperr.New(apperr.CodeConflict, "coupon sold out")
	case hotpath.DecisionCodeInactive:
		return claimmodel.Record{}, true, apperr.New(apperr.CodeBadRequest, "campaign not active")
	case hotpath.DecisionCodeAdmitted:
		rec, err := s.repo.PersistClaimAfterAdjudication(ctx, couponID, userID, idemKey)
		if err != nil {
			_ = s.adjudicator.Rollback(ctx, couponID, userID, idemKey, decision.ReservationID)
			return claimmodel.Record{}, true, s.mapClaimErr(err)
		}
		if err := s.adjudicator.Finalize(ctx, couponID, userID, idemKey, decision.ReservationID, rec.ID); err != nil {
			applog.L(ctx).Warn("finalize redis claim decision failed",
				zap.Int64("coupon_id", couponID),
				zap.Int64("user_id", userID),
				zap.Int64("claim_id", rec.ID),
				zap.Error(err),
			)
		}
		s.cacheClaim(ctx, rec)
		return rec, true, nil
	default:
		return claimmodel.Record{}, true, apperr.Wrap(apperr.CodeInternalError, "unknown redis adjudication result", fmt.Errorf("code=%s", decision.Code))
	}
}

func (s *Service) waitPendingClaim(ctx context.Context, couponID, userID int64, idemKey string) (claimmodel.Record, bool, error) {
	claimID, ok, err := s.adjudicator.WaitResult(ctx, couponID, userID, idemKey)
	if err != nil {
		return claimmodel.Record{}, false, apperr.Wrap(apperr.CodeInternalError, "wait pending claim result failed", err)
	}
	if !ok {
		return claimmodel.Record{}, false, nil
	}
	rec, err := s.repo.FindClaimByID(ctx, claimID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return claimmodel.Record{}, false, nil
		}
		return claimmodel.Record{}, false, apperr.Wrap(apperr.CodeInternalError, "load pending claim result failed", err)
	}
	return rec, true, nil
}

func (s *Service) mapClaimErr(err error) error {
	switch {
	case errors.Is(err, claimrepo.ErrAlreadyClaimed):
		return apperr.New(apperr.CodeConflict, "already claimed")
	case errors.Is(err, claimrepo.ErrClaimLimitReached):
		return apperr.New(apperr.CodeConflict, "claim limit reached")
	case errors.Is(err, claimrepo.ErrSoldOut):
		return apperr.New(apperr.CodeConflict, "coupon sold out")
	case errors.Is(err, claimrepo.ErrCampaignInactive):
		return apperr.New(apperr.CodeBadRequest, "campaign not active")
	case errors.Is(err, claimrepo.ErrCampaignNotFound):
		return apperr.New(apperr.CodeNotFound, "campaign not found")
	default:
		return apperr.Wrap(apperr.CodeInternalError, "claim coupon failed", err)
	}
}

func (s *Service) GetMyClaim(ctx context.Context, couponID, userID int64) (claimmodel.Record, error) {
	if rec, ok, err := s.loadCachedClaim(ctx, couponID, userID); err == nil && ok {
		return rec, nil
	} else if err != nil {
		applog.L(ctx).Warn("load cached claim failed",
			zap.Int64("coupon_id", couponID),
			zap.Int64("user_id", userID),
			zap.Error(err),
		)
	}
	rec, err := s.repo.FindClaimByUser(ctx, couponID, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return claimmodel.Record{}, apperr.New(apperr.CodeNotFound, "claim not found")
		}
		return claimmodel.Record{}, apperr.Wrap(apperr.CodeInternalError, "query claim failed", err)
	}
	s.cacheClaim(ctx, rec)
	return rec, nil
}

func ClaimReservationID() string {
	var buf [16]byte
	if _, err := cryptorand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), reservationIDFallback.Add(1))
}
