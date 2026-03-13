package coupon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"time"

	apperr "mini-jupiter/pkg/errors"
	applog "mini-jupiter/pkg/log"
	"mini-jupiter/pkg/redis"

	"go.uber.org/zap"
)

type Service struct {
	repo        *Repository
	adjudicator *Adjudicator
}

func NewService(repo *Repository, cache *redis.Client, idemTTL time.Duration) *Service {
	return &Service{
		repo:        repo,
		adjudicator: NewAdjudicator(cache),
	}
}

func (s *Service) Claim(ctx context.Context, couponID, userID int64, idemKey string) (ClaimRecord, error) {
	if s.adjudicator == nil {
		return s.claimWithoutRedis(ctx, couponID, userID, idemKey)
	}

	for attempt := 0; attempt < 2; attempt++ {
		rec, done, err := s.claimWithRedis(ctx, couponID, userID, idemKey)
		if done {
			return rec, err
		}
		if err != nil {
			return ClaimRecord{}, err
		}
	}
	return ClaimRecord{}, apperr.New(apperr.CodeTooManyRequests, "request is still being processed")
}

func (s *Service) claimWithoutRedis(ctx context.Context, couponID, userID int64, idemKey string) (ClaimRecord, error) {
	rec, err := s.repo.ClaimCoupon(ctx, couponID, userID, idemKey)
	if err == nil {
		return rec, nil
	}
	return ClaimRecord{}, s.mapClaimErr(ctx, couponID, userID, idemKey, err)
}

func (s *Service) claimWithRedis(ctx context.Context, couponID, userID int64, idemKey string) (ClaimRecord, bool, error) {
	reservationID := claimReservationID()
	decision, err := s.adjudicator.Decide(ctx, campaignSnapshot{CouponID: couponID}, userID, idemKey, time.Now().UTC(), reservationID)
	if err != nil {
		return ClaimRecord{}, true, apperr.Wrap(apperr.CodeInternalError, "redis adjudication failed", err)
	}

	switch decision.Code {
	case decisionCodeCampaignMiss:
		campaign, err := s.repo.LoadCampaign(ctx, couponID)
		if err != nil {
			return ClaimRecord{}, true, s.mapClaimErr(ctx, couponID, userID, idemKey, err)
		}
		if err := s.adjudicator.EnsureCampaign(ctx, campaign); err != nil {
			return ClaimRecord{}, true, apperr.Wrap(apperr.CodeInternalError, "hydrate redis campaign failed", err)
		}
		return ClaimRecord{}, false, nil
	case decisionCodeIdemHit:
		rec, err := s.repo.FindClaimByID(ctx, decision.ClaimID)
		if err != nil {
			return ClaimRecord{}, true, apperr.Wrap(apperr.CodeInternalError, "query replay claim failed", err)
		}
		return rec, true, nil
	case decisionCodePending:
		rec, ok, err := s.waitPendingClaim(ctx, couponID, userID, idemKey)
		if err != nil {
			return ClaimRecord{}, true, err
		}
		if ok {
			return rec, true, nil
		}
		return ClaimRecord{}, false, nil
	case decisionCodeAlready:
		return ClaimRecord{}, true, apperr.New(apperr.CodeConflict, "already claimed")
	case decisionCodeLimit:
		return ClaimRecord{}, true, apperr.New(apperr.CodeConflict, "claim limit reached")
	case decisionCodeSoldOut:
		return ClaimRecord{}, true, apperr.New(apperr.CodeConflict, "coupon sold out")
	case decisionCodeInactive:
		return ClaimRecord{}, true, apperr.New(apperr.CodeBadRequest, "campaign not active")
	case decisionCodeAdmitted:
		rec, err := s.repo.PersistClaimAfterAdjudication(ctx, couponID, userID, idemKey)
		if err != nil {
			_ = s.adjudicator.Rollback(ctx, couponID, userID, idemKey, decision.ReservationID)
			return ClaimRecord{}, true, s.mapClaimErr(ctx, couponID, userID, idemKey, err)
		}
		if err := s.adjudicator.Finalize(ctx, couponID, userID, idemKey, decision.ReservationID, rec.ID); err != nil {
			applog.L(ctx).Warn("finalize redis claim decision failed",
				zap.Int64("coupon_id", couponID),
				zap.Int64("user_id", userID),
				zap.Int64("claim_id", rec.ID),
				zap.Error(err),
			)
		}
		return rec, true, nil
	default:
		return ClaimRecord{}, true, apperr.Wrap(apperr.CodeInternalError, "unknown redis adjudication result", fmt.Errorf("code=%s", decision.Code))
	}
}

func (s *Service) waitPendingClaim(ctx context.Context, couponID, userID int64, idemKey string) (ClaimRecord, bool, error) {
	claimID, ok, err := s.adjudicator.WaitResult(ctx, couponID, userID, idemKey)
	if err != nil {
		return ClaimRecord{}, false, apperr.Wrap(apperr.CodeInternalError, "wait pending claim result failed", err)
	}
	if !ok {
		return ClaimRecord{}, false, nil
	}
	rec, err := s.repo.FindClaimByID(ctx, claimID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClaimRecord{}, false, nil
		}
		return ClaimRecord{}, false, apperr.Wrap(apperr.CodeInternalError, "load pending claim result failed", err)
	}
	return rec, true, nil
}

func (s *Service) mapClaimErr(ctx context.Context, couponID, userID int64, idemKey string, err error) error {
	switch {
	case errors.Is(err, ErrAlreadyClaimed):
		return apperr.New(apperr.CodeConflict, "already claimed")
	case errors.Is(err, ErrClaimLimitReached):
		return apperr.New(apperr.CodeConflict, "claim limit reached")
	case errors.Is(err, ErrSoldOut):
		return apperr.New(apperr.CodeConflict, "coupon sold out")
	case errors.Is(err, ErrCampaignInactive):
		return apperr.New(apperr.CodeBadRequest, "campaign not active")
	case errors.Is(err, ErrCampaignNotFound):
		return apperr.New(apperr.CodeNotFound, "campaign not found")
	default:
		return apperr.Wrap(apperr.CodeInternalError, "claim coupon failed", err)
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

func claimReservationID() string {
	return strconv.FormatInt(time.Now().UnixNano()+rand.Int63n(1000), 10)
}
