package main

import (
	"math"
	"sort"
	"time"
)

// LatencyStats holds percentile and aggregate statistics for chunk download latencies.
type LatencyStats struct {
	Min  time.Duration `json:"min_ms"`
	Max  time.Duration `json:"max_ms"`
	Mean time.Duration `json:"mean_ms"`
	P50  time.Duration `json:"p50_ms"`
	P95  time.Duration `json:"p95_ms"`
	P99  time.Duration `json:"p99_ms"`
}

// RunSummary contains the aggregate benchmark results for a single run.
type RunSummary struct {
	RunNumber    int          `json:"run"`
	ObjectSize   int64        `json:"object_size_bytes"`
	TotalBytes   int64        `json:"total_bytes_downloaded"`
	ChunkCount   int          `json:"chunk_count"`
	ChunkSize    int64        `json:"chunk_size_bytes"`
	Concurrency  int          `json:"concurrency"`
	TotalTime    time.Duration `json:"total_time_ms"`
	TTFB         time.Duration `json:"ttfb_ms"`
	ThroughputMB float64      `json:"throughput_mb_s"`
	ThroughputGB float64      `json:"throughput_gb_s"`
	ChunkLatency LatencyStats `json:"chunk_latency"`
}

// ConcurrencySweep holds all runs for a single concurrency level.
type ConcurrencySweep struct {
	Concurrency int
	Summaries   []RunSummary
	Aggregate   AggregateSummary
}

// AggregateSummary holds min/max/mean throughput across multiple runs.
type AggregateSummary struct {
	Runs         int     `json:"runs"`
	MinThroughputMB float64 `json:"min_throughput_mb_s"`
	MaxThroughputMB float64 `json:"max_throughput_mb_s"`
	MeanThroughputMB float64 `json:"mean_throughput_mb_s"`
	MinThroughputGB float64 `json:"min_throughput_gb_s"`
	MaxThroughputGB float64 `json:"max_throughput_gb_s"`
	MeanThroughputGB float64 `json:"mean_throughput_gb_s"`
}

// computeStats builds a RunSummary from a completed DownloadResult.
func computeStats(result DownloadResult, cfg *Config, objectSize int64, runNumber int, concurrency int) RunSummary {
	var totalBytes int64
	durations := make([]float64, 0, len(result.Chunks))

	for _, c := range result.Chunks {
		totalBytes += c.Size
		durations = append(durations, float64(c.ElapsedTotal))
	}

	sort.Float64s(durations)

	n := len(durations)
	sum := 0.0
	for _, d := range durations {
		sum += d
	}

	percentile := func(p float64) time.Duration {
		if n == 0 {
			return 0
		}
		idx := int(math.Ceil(p/100.0*float64(n))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= n {
			idx = n - 1
		}
		return time.Duration(durations[idx])
	}

	var minD, maxD, meanD time.Duration
	if n > 0 {
		minD = time.Duration(durations[0])
		maxD = time.Duration(durations[n-1])
		meanD = time.Duration(sum / float64(n))
	}

	elapsed := result.TotalTime.Seconds()
	var throughputMB, throughputGB float64
	if elapsed > 0 {
		throughputMB = float64(totalBytes) / (1 << 20) / elapsed
		throughputGB = float64(totalBytes) / (1 << 30) / elapsed
	}

	return RunSummary{
		RunNumber:   runNumber,
		ObjectSize:  objectSize,
		TotalBytes:  totalBytes,
		ChunkCount:  len(result.Chunks),
		ChunkSize:   cfg.ChunkSize,
		Concurrency: concurrency,
		TotalTime:   result.TotalTime,
		TTFB:        result.TTFB,
		ThroughputMB: throughputMB,
		ThroughputGB: throughputGB,
		ChunkLatency: LatencyStats{
			Min:  minD,
			Max:  maxD,
			Mean: meanD,
			P50:  percentile(50),
			P95:  percentile(95),
			P99:  percentile(99),
		},
	}
}

// computeAggregate summarises throughput statistics across multiple runs.
func computeAggregate(summaries []RunSummary) AggregateSummary {
	if len(summaries) == 0 {
		return AggregateSummary{}
	}

	minMB := summaries[0].ThroughputMB
	maxMB := summaries[0].ThroughputMB
	sumMB := 0.0

	for _, s := range summaries {
		if s.ThroughputMB < minMB {
			minMB = s.ThroughputMB
		}
		if s.ThroughputMB > maxMB {
			maxMB = s.ThroughputMB
		}
		sumMB += s.ThroughputMB
	}

	meanMB := sumMB / float64(len(summaries))

	return AggregateSummary{
		Runs:             len(summaries),
		MinThroughputMB:  minMB,
		MaxThroughputMB:  maxMB,
		MeanThroughputMB: meanMB,
		MinThroughputGB:  minMB / 1024,
		MaxThroughputGB:  maxMB / 1024,
		MeanThroughputGB: meanMB / 1024,
	}
}
