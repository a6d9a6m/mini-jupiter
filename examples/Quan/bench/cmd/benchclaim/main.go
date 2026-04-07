package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		scenario = flag.String("scenario", "steady_success", "scenario label")
		baseURL  = flag.String("base-url", "http://127.0.0.1:8081", "service base url")
		couponID = flag.Int64("coupon-id", 9001, "coupon id")

		requests    = flag.Int("requests", 6000, "total request count")
		concurrency = flag.Int("concurrency", 80, "worker concurrency")
		timeout     = flag.Duration("timeout", 3*time.Second, "request timeout")

		userMode    = flag.String("user-mode", "unique", "user id mode: unique|cycle")
		userPool    = flag.Int("user-pool", 200, "user pool size for cycle mode")
		startUserID = flag.Int64("start-user-id", 100000, "starting user id")

		idemMode     = flag.String("idem-mode", "unique", "idempotency key mode: unique|per_user|fixed")
		idemPrefix   = flag.String("idem-prefix", "bench", "idempotency key prefix")
		fixedIdemKey = flag.String("fixed-idem-key", "", "fixed idempotency key when idem-mode=fixed")

		reportOut = flag.String("report-out", "", "optional output json path")
	)
	flag.Parse()

	if *couponID <= 0 {
		fail("invalid -coupon-id, must be > 0")
	}
	if *requests <= 0 {
		fail("invalid -requests, must be > 0")
	}
	if *concurrency <= 0 {
		fail("invalid -concurrency, must be > 0")
	}
	if *concurrency > *requests {
		*concurrency = *requests
	}
	if *userMode != "unique" && *userMode != "cycle" {
		fail("invalid -user-mode, only unique|cycle")
	}
	if *userPool <= 0 {
		fail("invalid -user-pool, must be > 0")
	}
	if *idemMode != "unique" && *idemMode != "per_user" && *idemMode != "fixed" {
		fail("invalid -idem-mode, only unique|per_user|fixed")
	}
	if *idemMode == "fixed" && strings.TrimSpace(*fixedIdemKey) == "" {
		fail("missing -fixed-idem-key when idem-mode=fixed")
	}

	params := runParams{
		Scenario:     *scenario,
		BaseURL:      strings.TrimRight(strings.TrimSpace(*baseURL), "/"),
		CouponID:     *couponID,
		Requests:     *requests,
		Concurrency:  *concurrency,
		TimeoutMs:    timeout.Milliseconds(),
		UserMode:     *userMode,
		UserPool:     *userPool,
		StartUserID:  *startUserID,
		IdemMode:     *idemMode,
		IdemPrefix:   *idemPrefix,
		FixedIdemKey: *fixedIdemKey,
	}
	result := runBenchmark(params, *timeout)

	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fail("marshal run result failed: %v", err)
	}
	fmt.Println(string(raw))

	if strings.TrimSpace(*reportOut) != "" {
		if err := os.WriteFile(*reportOut, raw, 0o644); err != nil {
			fail("write report file failed: %v", err)
		}
	}
}

func fail(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
