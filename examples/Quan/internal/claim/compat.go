package claim

import (
	"database/sql"
	"time"

	"mini-jupiter/examples/Quan/internal/adjudication/hotpath"
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

var (
	ErrCampaignNotFound  = claimrepo.ErrCampaignNotFound
	ErrCampaignInactive  = claimrepo.ErrCampaignInactive
	ErrSoldOut           = claimrepo.ErrSoldOut
	ErrAlreadyClaimed    = claimrepo.ErrAlreadyClaimed
	ErrClaimLimitReached = claimrepo.ErrClaimLimitReached
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

func ClaimReservationID() string {
	return claimservice.ClaimReservationID()
}

func ClaimCacheKey(couponID, userID int64) string {
	return claimservice.ClaimCacheKey(couponID, userID)
}
