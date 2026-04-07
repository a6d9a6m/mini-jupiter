package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	claimrequest "mini-jupiter/examples/Quan/internal/claim/request"
	appredis "mini-jupiter/pkg/redis"

	_ "github.com/go-sql-driver/mysql"
)

type reportEnvelope struct {
	Scenario string `json:"scenario"`
	Business struct {
		Success int `json:"success"`
		Total   int `json:"total"`
	} `json:"business"`
	HTTPStatusCounts map[string]int `json:"http_status_counts"`
	TransportErrors  map[string]int `json:"transport_errors"`
}

type auditResult struct {
	CouponID  int64           `json:"coupon_id"`
	CheckedAt string          `json:"checked_at"`
	Campaign  campaignAudit   `json:"campaign"`
	Claims    claimAudit      `json:"claims"`
	Requests  *requestAudit   `json:"requests,omitempty"`
	Benchmark *benchmarkAudit `json:"benchmark,omitempty"`
	Verdict   auditVerdict    `json:"verdict"`
}

type campaignAudit struct {
	Status         string `json:"status"`
	TotalStock     int    `json:"total_stock"`
	AvailableStock int    `json:"available_stock"`
	PerUserLimit   int    `json:"per_user_limit"`
}

type claimAudit struct {
	PersistedClaims  int `json:"persisted_claims"`
	DistinctUsers    int `json:"distinct_users"`
	MaxClaimsPerUser int `json:"max_claims_per_user"`
	OverflowUsers    int `json:"overflow_users"`
	OverflowClaims   int `json:"overflow_claims"`
	OversellClaims   int `json:"oversell_claims"`
}

type requestAudit struct {
	Prefix                   string         `json:"prefix"`
	Total                    int            `json:"total"`
	StatusCounts             map[string]int `json:"status_counts"`
	Succeeded                int            `json:"succeeded"`
	Failed                   int            `json:"failed"`
	RolledBack               int            `json:"rolled_back"`
	InFlight                 int            `json:"in_flight"`
	StaleInFlight            int            `json:"stale_in_flight"`
	SucceededWithoutClaim    int            `json:"succeeded_without_claim"`
	TerminalFailureWithClaim int            `json:"terminal_failure_with_claim"`
	SuccessDeltaVsClaims     int            `json:"success_delta_vs_claims"`
}

type benchmarkAudit struct {
	Scenario                string `json:"scenario"`
	ReportPath              string `json:"report_path"`
	AcceptedResponses       int    `json:"accepted_responses"`
	ConflictResponses       int    `json:"conflict_responses"`
	TransportErrors         int    `json:"transport_errors"`
	AcceptedDeltaVsRequests int    `json:"accepted_delta_vs_requests"`
}

type auditVerdict struct {
	Pass    bool     `json:"pass"`
	Reasons []string `json:"reasons"`
}

func main() {
	var (
		dsn               = flag.String("dsn", "", "mysql dsn")
		couponID          = flag.Int64("coupon-id", 0, "coupon id")
		reportPath        = flag.String("report-path", "", "optional benchmark report json path")
		redisEnabled      = flag.Bool("redis-enabled", true, "whether to audit Redis request ledger")
		redisMode         = flag.String("redis-mode", appredis.ModeSentinel, "redis mode: simple or sentinel")
		redisAddr         = flag.String("redis-addr", "127.0.0.1:6379", "redis addr for simple mode")
		redisAddrs        = flag.String("redis-addrs", "127.0.0.1:26379,127.0.0.1:26380,127.0.0.1:26381", "comma-separated redis sentinel addrs")
		redisMasterName   = flag.String("redis-master-name", "mymaster", "redis sentinel master name")
		redisPassword     = flag.String("redis-password", "", "redis password")
		requestPrefix     = flag.String("request-prefix", "quan:claim", "redis request prefix")
		requestStaleAfter = flag.Duration("request-stale-after", 30*time.Second, "non-terminal request stale threshold")
	)
	flag.Parse()

	if *dsn == "" {
		fail("missing required -dsn")
	}
	if *couponID <= 0 {
		fail("invalid -coupon-id, must be > 0")
	}

	db, err := sql.Open("mysql", *dsn)
	if err != nil {
		fail("open mysql failed: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		fail("ping mysql failed: %v", err)
	}

	var redisClient *appredis.Client
	if *redisEnabled {
		redisClient, err = appredis.NewClient(appredis.Config{
			Enabled:     true,
			Mode:        *redisMode,
			Addr:        *redisAddr,
			Addrs:       splitCSV(*redisAddrs),
			MasterName:  *redisMasterName,
			Password:    *redisPassword,
			DialTimeout: 2 * time.Second,
		})
		if err != nil {
			fail("new redis client failed: %v", err)
		}
		defer redisClient.Close()
		if err := redisClient.Ping(ctx); err != nil {
			fail("ping redis failed: %v", err)
		}
	}

	result, err := runAudit(ctx, db, redisClient, *couponID, *requestPrefix, *requestStaleAfter, *reportPath)
	if err != nil {
		fail("run audit failed: %v", err)
	}

	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fail("marshal audit result failed: %v", err)
	}
	fmt.Println(string(raw))
	if !result.Verdict.Pass {
		os.Exit(2)
	}
}

func runAudit(ctx context.Context, db *sql.DB, redisClient *appredis.Client, couponID int64, requestPrefix string, requestStaleAfter time.Duration, reportPath string) (auditResult, error) {
	campaign, err := loadCampaign(ctx, db, couponID)
	if err != nil {
		return auditResult{}, err
	}
	claims, err := loadClaims(ctx, db, couponID, campaign.TotalStock, campaign.PerUserLimit)
	if err != nil {
		return auditResult{}, err
	}

	var requests *requestAudit
	if redisClient != nil {
		reqAudit, err := loadRequests(ctx, redisClient, couponID, requestPrefix, requestStaleAfter, claims.PersistedClaims)
		if err != nil {
			return auditResult{}, err
		}
		requests = reqAudit
	}

	var bench *benchmarkAudit
	if reportPath != "" {
		bench, err = loadBenchmark(reportPath, requests)
		if err != nil {
			return auditResult{}, err
		}
	}

	verdict := auditVerdict{Pass: true}
	if claims.OversellClaims > 0 {
		verdict.Pass = false
		verdict.Reasons = append(verdict.Reasons, fmt.Sprintf("oversell detected: %d claims above stock", claims.OversellClaims))
	}
	if claims.OverflowClaims > 0 {
		verdict.Pass = false
		verdict.Reasons = append(verdict.Reasons, fmt.Sprintf("per-user limit overflow detected: %d excess claim(s)", claims.OverflowClaims))
	}
	if requests != nil {
		if requests.SuccessDeltaVsClaims != 0 {
			verdict.Pass = false
			verdict.Reasons = append(verdict.Reasons, fmt.Sprintf("request success delta vs claims: %d", requests.SuccessDeltaVsClaims))
		}
		if requests.SucceededWithoutClaim > 0 {
			verdict.Pass = false
			verdict.Reasons = append(verdict.Reasons, fmt.Sprintf("succeeded requests without persisted claim: %d", requests.SucceededWithoutClaim))
		}
		if requests.TerminalFailureWithClaim > 0 {
			verdict.Pass = false
			verdict.Reasons = append(verdict.Reasons, fmt.Sprintf("failed or rolled_back requests still carrying claim_id: %d", requests.TerminalFailureWithClaim))
		}
		if requests.StaleInFlight > 0 {
			verdict.Pass = false
			verdict.Reasons = append(verdict.Reasons, fmt.Sprintf("stale non-terminal requests detected: %d", requests.StaleInFlight))
		}
	}
	if bench != nil && bench.AcceptedDeltaVsRequests != 0 {
		verdict.Pass = false
		verdict.Reasons = append(verdict.Reasons, fmt.Sprintf("benchmark accepted delta vs requests: %d", bench.AcceptedDeltaVsRequests))
	}
	if verdict.Pass {
		verdict.Reasons = append(verdict.Reasons, "request ledger, mq handoff, and final claims are consistent for the audited coupon")
	}

	return auditResult{
		CouponID:  couponID,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Campaign:  campaign,
		Claims:    claims,
		Requests:  requests,
		Benchmark: bench,
		Verdict:   verdict,
	}, nil
}

func loadCampaign(ctx context.Context, db *sql.DB, couponID int64) (campaignAudit, error) {
	var out campaignAudit
	err := db.QueryRowContext(ctx, `
SELECT status, total_stock, available_stock, per_user_limit
FROM coupon_campaigns
WHERE coupon_id = ?
LIMIT 1
`, couponID).Scan(&out.Status, &out.TotalStock, &out.AvailableStock, &out.PerUserLimit)
	if err != nil {
		return campaignAudit{}, err
	}
	if out.PerUserLimit <= 0 {
		out.PerUserLimit = 1
	}
	return out, nil
}

func loadClaims(ctx context.Context, db *sql.DB, couponID int64, totalStock, perUserLimit int) (claimAudit, error) {
	var out claimAudit
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM coupon_claims WHERE coupon_id = ?`, couponID).Scan(&out.PersistedClaims); err != nil {
		return claimAudit{}, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT user_id) FROM coupon_claims WHERE coupon_id = ?`, couponID).Scan(&out.DistinctUsers); err != nil {
		return claimAudit{}, err
	}
	if err := db.QueryRowContext(ctx, `
SELECT COALESCE(MAX(cnt), 0)
FROM (
  SELECT COUNT(1) AS cnt
  FROM coupon_claims
  WHERE coupon_id = ?
  GROUP BY user_id
) AS claim_counts
`, couponID).Scan(&out.MaxClaimsPerUser); err != nil {
		return claimAudit{}, err
	}
	if err := db.QueryRowContext(ctx, `
SELECT COALESCE(COUNT(1), 0), COALESCE(SUM(cnt - ?), 0)
FROM (
  SELECT COUNT(1) AS cnt
  FROM coupon_claims
  WHERE coupon_id = ?
  GROUP BY user_id
  HAVING COUNT(1) > ?
) AS overflow_counts
`, perUserLimit, couponID, perUserLimit).Scan(&out.OverflowUsers, &out.OverflowClaims); err != nil {
		return claimAudit{}, err
	}
	if out.PersistedClaims > totalStock {
		out.OversellClaims = out.PersistedClaims - totalStock
	}
	return out, nil
}

func loadRequests(ctx context.Context, redisClient *appredis.Client, couponID int64, prefix string, staleAfter time.Duration, persistedClaims int) (*requestAudit, error) {
	out := &requestAudit{
		Prefix:       prefix,
		StatusCounts: map[string]int{},
	}
	knownStatuses := []claimrequest.Status{
		claimrequest.StatusAccepted,
		claimrequest.StatusPublishing,
		claimrequest.StatusEnqueued,
		claimrequest.StatusProcessing,
		claimrequest.StatusSucceeded,
		claimrequest.StatusRolledBack,
		claimrequest.StatusFailed,
	}
	seen := make(map[string]struct{})
	now := time.Now().UTC()
	for _, status := range knownStatuses {
		ids, err := redisClient.Raw().ZRange(ctx, requestStatusKey(prefix, status), 0, -1).Result()
		if err != nil {
			return nil, fmt.Errorf("load request ids for status %s: %w", status, err)
		}
		for _, requestID := range ids {
			if _, ok := seen[requestID]; ok {
				continue
			}
			seen[requestID] = struct{}{}

			fields, err := redisClient.Raw().HGetAll(ctx, requestKey(prefix, requestID)).Result()
			if err != nil {
				return nil, fmt.Errorf("load request %s: %w", requestID, err)
			}
			if len(fields) == 0 {
				continue
			}
			reqCouponID, err := parseInt64(fields["coupon_id"])
			if err != nil || reqCouponID != couponID {
				continue
			}
			reqStatus := fields["status"]
			out.Total++
			out.StatusCounts[reqStatus]++

			claimID, _ := parseOptionalInt64(fields["claim_id"])
			switch claimrequest.Status(reqStatus) {
			case claimrequest.StatusSucceeded:
				out.Succeeded++
				if claimID <= 0 {
					out.SucceededWithoutClaim++
				}
			case claimrequest.StatusFailed:
				out.Failed++
				if claimID > 0 {
					out.TerminalFailureWithClaim++
				}
			case claimrequest.StatusRolledBack:
				out.RolledBack++
				if claimID > 0 {
					out.TerminalFailureWithClaim++
				}
			default:
				out.InFlight++
				updatedAt, parseErr := parseUnixMilli(fields["updated_at_ms"])
				if parseErr == nil && staleAfter > 0 && now.Sub(updatedAt) > staleAfter {
					out.StaleInFlight++
				}
			}
		}
	}
	out.SuccessDeltaVsClaims = out.Succeeded - persistedClaims
	return out, nil
}

func loadBenchmark(path string, requests *requestAudit) (*benchmarkAudit, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var env reportEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	out := &benchmarkAudit{
		Scenario:          env.Scenario,
		ReportPath:        path,
		AcceptedResponses: env.Business.Success,
		ConflictResponses: env.HTTPStatusCounts["409"],
	}
	for _, cnt := range env.TransportErrors {
		out.TransportErrors += cnt
	}
	if requests != nil {
		out.AcceptedDeltaVsRequests = env.Business.Success - requests.Total
	}
	return out, nil
}

func requestStatusKey(prefix string, status claimrequest.Status) string {
	return fmt.Sprintf("%s:request:status:%s", prefix, status)
}

func requestKey(prefix, requestID string) string {
	return fmt.Sprintf("%s:request:%s", prefix, requestID)
}

func parseInt64(v string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(v), 10, 64)
}

func parseOptionalInt64(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "0" {
		return 0, nil
	}
	return strconv.ParseInt(v, 10, 64)
}

func parseUnixMilli(v string) (time.Time, error) {
	ms, err := parseInt64(v)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms).UTC(), nil
}

func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func fail(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
