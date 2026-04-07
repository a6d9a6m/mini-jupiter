package claim

import (
	"context"
	"database/sql"
	"time"

	"mini-jupiter/examples/Quan/internal/adjudication/hotpath"
	"mini-jupiter/examples/Quan/internal/adjudication/reservation"
	claimmodel "mini-jupiter/examples/Quan/internal/claim/model"
	claimrepo "mini-jupiter/examples/Quan/internal/claim/repository"
	claimservice "mini-jupiter/examples/Quan/internal/claim/service"
	"mini-jupiter/pkg/mysql"
	"mini-jupiter/pkg/redis"
)

type ClaimRecord = claimmodel.Record
type Repository = claimrepo.Repository
type Service = claimservice.Service
type Adjudicator = hotpath.Adjudicator
type ReservationReconciler = reservation.ReservationReconciler
type ReservationReconcilerConfig = reservation.ReservationReconcilerConfig

type campaignSnapshot = hotpath.CampaignSnapshot
type reservationLease = hotpath.ReservationLease

type reservationReconcilerRepository interface {
	FindClaimByIdempotency(ctx context.Context, couponID, userID int64, idemKey string) (claimmodel.Record, bool, error)
}

type reservationReconcilerAdjudicator interface {
	ListExpiredReservations(ctx context.Context, now time.Time, limit int) ([]hotpath.ReservationLease, error)
	Finalize(ctx context.Context, couponID, userID int64, idemKey, reservationID string, claimID int64) error
	Rollback(ctx context.Context, couponID, userID int64, idemKey, reservationID string) error
}

var (
	ErrCampaignNotFound  = claimrepo.ErrCampaignNotFound
	ErrCampaignInactive  = claimrepo.ErrCampaignInactive
	ErrSoldOut           = claimrepo.ErrSoldOut
	ErrAlreadyClaimed    = claimrepo.ErrAlreadyClaimed
	ErrClaimLimitReached = claimrepo.ErrClaimLimitReached
)

const (
	decisionCodeAdmitted     = hotpath.DecisionCodeAdmitted
	decisionCodeIdemHit      = hotpath.DecisionCodeIdemHit
	decisionCodePending      = hotpath.DecisionCodePending
	decisionCodeAlready      = hotpath.DecisionCodeAlready
	decisionCodeLimit        = hotpath.DecisionCodeLimit
	decisionCodeSoldOut      = hotpath.DecisionCodeSoldOut
	decisionCodeInactive     = hotpath.DecisionCodeInactive
	decisionCodeCampaignMiss = hotpath.DecisionCodeCampaignMiss
)

func NewRepository(db *sql.DB, txm *mysql.TxManager) *Repository {
	return claimrepo.NewRepository(db, txm)
}

func NewService(repo *Repository, cache *redis.Client, idemTTL time.Duration) *Service {
	return claimservice.NewService(repo, cache, idemTTL)
}

func NewServiceWithAdjudicator(repo *Repository, adjudicator *Adjudicator, idemTTL time.Duration) *Service {
	return claimservice.NewServiceWithAdjudicator(repo, adjudicator, idemTTL)
}

func NewAdjudicator(client *redis.Client) *Adjudicator {
	return hotpath.NewAdjudicator(client)
}

func NewReservationReconciler(repo reservationReconcilerRepository, adjudicator reservationReconcilerAdjudicator, cfg ReservationReconcilerConfig) (*ReservationReconciler, error) {
	return reservation.NewReservationReconciler(repo, adjudicator, cfg)
}

func ClaimReservationID() string {
	return claimservice.ClaimReservationID()
}

func claimReservationID() string {
	return ClaimReservationID()
}

func ClaimCacheKey(couponID, userID int64) string {
	return claimservice.ClaimCacheKey(couponID, userID)
}

func claimCacheKey(couponID, userID int64) string {
	return ClaimCacheKey(couponID, userID)
}

func campaignMetaKey(couponID int64) string {
	return hotpath.CampaignMetaKey(couponID)
}

func campaignStockKey(couponID int64) string {
	return hotpath.CampaignStockKey(couponID)
}

func campaignUserCountKey(couponID int64) string {
	return hotpath.CampaignUserCountKey(couponID)
}

func idemDecisionKey(couponID, userID int64, idemKey string) string {
	return hotpath.IdemDecisionKey(couponID, userID, idemKey)
}

func reservationLeaseKey(reservationID string) string {
	return hotpath.ReservationLeaseKey(reservationID)
}
