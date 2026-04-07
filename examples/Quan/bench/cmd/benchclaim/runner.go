package main

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

func runBenchmark(params runParams, timeout time.Duration) runResult {
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConns:        params.Concurrency * 2,
			MaxIdleConnsPerHost: params.Concurrency * 2,
			MaxConnsPerHost:     params.Concurrency * 2,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	targetURL := buildTargetURL(params)

	results := make(chan singleResult, params.Concurrency*2)
	jobs := make(chan int, params.Concurrency*2)
	var wg sync.WaitGroup

	startedAt := time.Now().UTC()
	for i := 0; i < params.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				results <- runSingleRequest(client, targetURL, params, idx)
			}
		}()
	}

	go func() {
		for i := 0; i < params.Requests; i++ {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	httpStatusCounts := map[string]int{}
	appCodeCounts := map[string]int{}
	transportErrors := map[string]int{}
	latencies := make([]int64, 0, params.Requests)
	bizTotal := 0
	bizSuccess := 0

	for rec := range results {
		if rec.errText != "" {
			transportErrors[shortErr(rec.errText)]++
			continue
		}
		latencies = append(latencies, rec.latencyNs)
		httpStatusCounts[strconv.Itoa(rec.httpCode)]++
		appCodeCounts[strconv.Itoa(rec.appCode)]++
		bizTotal++
		if rec.appCode == 0 {
			bizSuccess++
		}
	}

	finishedAt := time.Now().UTC()
	durationSec := finishedAt.Sub(startedAt).Seconds()
	if durationSec <= 0 {
		durationSec = 0.001
	}

	return runResult{
		Scenario:         params.Scenario,
		StartedAt:        startedAt.Format(time.RFC3339),
		FinishedAt:       finishedAt.Format(time.RFC3339),
		DurationSeconds:  durationSec,
		QPS:              float64(len(latencies)) / durationSec,
		Params:           params,
		LatencyMS:        calcLatency(latencies),
		HTTPStatusCounts: httpStatusCounts,
		AppCodeCounts:    appCodeCounts,
		TransportErrors:  transportErrors,
		Business: businessStats{
			Success:     bizSuccess,
			Total:       bizTotal,
			SuccessRate: ratio(bizSuccess, bizTotal),
		},
	}
}

func buildTargetURL(params runParams) string {
	return params.BaseURL + "/api/v1/coupons/" + strconv.FormatInt(params.CouponID, 10) + "/claim"
}

func runSingleRequest(client *http.Client, targetURL string, params runParams, idx int) singleResult {
	userID := generateUserID(idx, params.UserMode, params.StartUserID, params.UserPool)
	idemKey := generateIdemKey(idx, userID, params.IdemMode, params.IdemPrefix, params.FixedIdemKey)
	start := time.Now()
	req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(nil))
	if err != nil {
		return singleResult{errText: err.Error()}
	}
	req.Header.Set("X-User-ID", strconv.FormatInt(userID, 10))
	req.Header.Set("Idempotency-Key", idemKey)

	resp, err := client.Do(req)
	if err != nil {
		return singleResult{
			latencyNs: time.Since(start).Nanoseconds(),
			errText:   err.Error(),
		}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return singleResult{
		latencyNs: time.Since(start).Nanoseconds(),
		httpCode:  resp.StatusCode,
		appCode:   parseAppCode(body),
	}
}
