package request

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"

	"mini-jupiter/examples/Quan/internal/adjudication/hotpath"
	claimrepo "mini-jupiter/examples/Quan/internal/claim/repository"
	apperr "mini-jupiter/pkg/errors"
)

type RedisAdmitter struct {
	repo       *claimrepo.Repository
	adjudicate *hotpath.Adjudicator
}

func NewRedisAdmitter(repo *claimrepo.Repository, adjudicator *hotpath.Adjudicator) *RedisAdmitter {
	return &RedisAdmitter{repo: repo, adjudicate: adjudicator}
}

func (a *RedisAdmitter) Decide(ctx context.Context, couponID, userID int64, idemKey string) (Decision, error) {
	if a == nil || a.adjudicate == nil {
		return Decision{}, apperr.New(apperr.CodeInternalError, "redis adjudicator is not configured")
	}

	for attempt := 0; attempt < 2; attempt++ {
		requestID := newRequestID()
		decision, err := a.adjudicate.Decide(ctx, hotpath.CampaignSnapshot{CouponID: couponID}, userID, idemKey, time.Now().UTC(), requestID)
		if err != nil {
			return Decision{}, apperr.Wrap(apperr.CodeInternalError, "redis adjudication failed", err)
		}

		switch decision.Code {
		case hotpath.DecisionCodeCampaignMiss:
			if a.repo == nil {
				return Decision{}, apperr.New(apperr.CodeNotFound, "campaign not found")
			}
			campaign, err := a.repo.LoadCampaign(ctx, couponID)
			if err != nil {
				return Decision{}, mapClaimErr(err)
			}
			if err := a.adjudicate.EnsureCampaign(ctx, campaign); err != nil {
				return Decision{}, apperr.Wrap(apperr.CodeInternalError, "hydrate redis campaign failed", err)
			}
			continue
		case hotpath.DecisionCodeAdmitted:
			return Decision{Code: DecisionCodeAdmitted, RequestID: decision.ReservationID}, nil
		case hotpath.DecisionCodePending:
			return Decision{}, apperr.New(apperr.CodeTooManyRequests, "request is still being processed")
		case hotpath.DecisionCodeIdemHit:
			return Decision{}, apperr.New(apperr.CodeTooManyRequests, "request result should be polled by request_id")
		case hotpath.DecisionCodeAlready:
			return Decision{}, apperr.New(apperr.CodeConflict, "already claimed")
		case hotpath.DecisionCodeLimit:
			return Decision{}, apperr.New(apperr.CodeConflict, "claim limit reached")
		case hotpath.DecisionCodeSoldOut:
			return Decision{}, apperr.New(apperr.CodeConflict, "coupon sold out")
		case hotpath.DecisionCodeInactive:
			return Decision{}, apperr.New(apperr.CodeBadRequest, "campaign not active")
		default:
			return Decision{}, apperr.New(apperr.CodeInternalError, "unknown adjudication result")
		}
	}

	return Decision{}, apperr.New(apperr.CodeInternalError, "campaign hot state was not initialized")
}

func (a *RedisAdmitter) Finalize(ctx context.Context, couponID, userID int64, idemKey, requestID string, claimID int64) error {
	if a == nil || a.adjudicate == nil {
		return apperr.New(apperr.CodeInternalError, "redis adjudicator is not configured")
	}
	return a.adjudicate.Finalize(ctx, couponID, userID, idemKey, requestID, claimID)
}

func (a *RedisAdmitter) Rollback(ctx context.Context, couponID, userID int64, idemKey, requestID string) error {
	if a == nil || a.adjudicate == nil {
		return apperr.New(apperr.CodeInternalError, "redis adjudicator is not configured")
	}
	return a.adjudicate.Rollback(ctx, couponID, userID, idemKey, requestID)
}

type SQLClaimWriter struct {
	repo *claimrepo.Repository
}

func NewSQLClaimWriter(repo *claimrepo.Repository) *SQLClaimWriter {
	return &SQLClaimWriter{repo: repo}
}

func (w *SQLClaimWriter) PersistClaim(ctx context.Context, req Request) (int64, bool, error) {
	if w == nil || w.repo == nil {
		return 0, false, fmt.Errorf("claim writer repository is nil")
	}
	rec, inserted, err := w.repo.PersistClaimAsync(ctx, req.CouponID, req.UserID, req.IdempotencyKey)
	if err != nil {
		return 0, false, err
	}
	return rec.ID, inserted, nil
}

type SQLClaimLookup struct {
	repo *claimrepo.Repository
}

func NewSQLClaimLookup(repo *claimrepo.Repository) *SQLClaimLookup {
	return &SQLClaimLookup{repo: repo}
}

func (l *SQLClaimLookup) FindClaimID(ctx context.Context, req Request) (int64, bool, error) {
	if l == nil || l.repo == nil {
		return 0, false, fmt.Errorf("claim lookup repository is nil")
	}
	rec, found, err := l.repo.FindClaimByIdempotency(ctx, req.CouponID, req.UserID, req.IdempotencyKey)
	if err != nil {
		return 0, false, err
	}
	if !found {
		return 0, false, nil
	}
	return rec.ID, true, nil
}

func mapClaimErr(err error) error {
	switch err {
	case nil:
		return nil
	case claimrepo.ErrAlreadyClaimed:
		return apperr.New(apperr.CodeConflict, "already claimed")
	case claimrepo.ErrClaimLimitReached:
		return apperr.New(apperr.CodeConflict, "claim limit reached")
	case claimrepo.ErrSoldOut:
		return apperr.New(apperr.CodeConflict, "coupon sold out")
	case claimrepo.ErrCampaignInactive:
		return apperr.New(apperr.CodeBadRequest, "campaign not active")
	case claimrepo.ErrCampaignNotFound:
		return apperr.New(apperr.CodeNotFound, "campaign not found")
	default:
		return apperr.Wrap(apperr.CodeInternalError, "claim coupon failed", err)
	}
}

var requestIDFallback atomic.Uint64

func newRequestID() string {
	var buf [16]byte
	if _, err := cryptorand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), requestIDFallback.Add(1))
}
