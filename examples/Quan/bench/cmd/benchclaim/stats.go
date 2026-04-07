package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

func generateUserID(idx int, mode string, startUserID int64, userPool int) int64 {
	switch mode {
	case "cycle":
		return startUserID + int64(idx%userPool)
	default:
		return startUserID + int64(idx)
	}
}

func generateIdemKey(idx int, userID int64, mode, prefix, fixedKey string) string {
	switch mode {
	case "per_user":
		return fmt.Sprintf("%s-u%d", prefix, userID)
	case "fixed":
		return fixedKey
	default:
		return fmt.Sprintf("%s-%d", prefix, idx)
	}
}

func parseAppCode(body []byte) int {
	if len(body) == 0 {
		return -1
	}
	var env responseEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return -1
	}
	return env.Code
}

func calcLatency(samples []int64) latencyStats {
	if len(samples) == 0 {
		return latencyStats{}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	var sum int64
	for _, v := range samples {
		sum += v
	}
	return latencyStats{
		Min: nsToMS(samples[0]),
		Avg: nsToMS(float64(sum) / float64(len(samples))),
		P50: nsToMS(percentile(samples, 50)),
		P95: nsToMS(percentile(samples, 95)),
		P99: nsToMS(percentile(samples, 99)),
		Max: nsToMS(samples[len(samples)-1]),
	}
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	pos := int(math.Ceil((p/100.0)*float64(len(sorted)))) - 1
	if pos < 0 {
		pos = 0
	}
	if pos >= len(sorted) {
		pos = len(sorted) - 1
	}
	return sorted[pos]
}

func nsToMS(v any) float64 {
	switch n := v.(type) {
	case int64:
		return float64(n) / 1e6
	case float64:
		return n / 1e6
	default:
		return 0
	}
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func shortErr(errText string) string {
	s := strings.TrimSpace(errText)
	if s == "" {
		return "unknown"
	}
	if len(s) > 120 {
		return s[:120]
	}
	return s
}
