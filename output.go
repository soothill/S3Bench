// Copyright (c) 2026 Darren Soothill <darren [at] soothill [dot] com>
// All rights reserved.
// Use of this source code is governed by the MIT License.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// startProgressReporter spawns a goroutine that prints a live transfer-rate line
// every 200ms, overwriting itself with \r. Call the returned stop function when
// the download finishes; it clears the line so subsequent output is clean.
func startProgressReporter(totalBytes int64, progress *atomic.Int64) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)

		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		startTime := time.Now()
		prevBytes := int64(0)
		prevTime := startTime

		print := func(now time.Time) {
			cur := progress.Load()
			interval := now.Sub(prevTime).Seconds()

			var rateMB float64
			if interval > 0 {
				rateMB = float64(cur-prevBytes) / (1 << 20) / interval
			}

			pct := 0.0
			if totalBytes > 0 {
				pct = float64(cur) / float64(totalBytes) * 100
			}
			elapsed := now.Sub(startTime)

			line := fmt.Sprintf("  %s / %s  (%5.1f%%)   %8.1f MB/s   elapsed: %s",
				formatBytes(cur), formatBytes(totalBytes), pct, rateMB, formatDuration(elapsed))
			// %-80s pads to 80 chars so any shorter line fully overwrites a longer previous one.
			fmt.Printf("\r%-80s", line)

			prevBytes = cur
			prevTime = now
		}

		for {
			select {
			case <-done:
				// Clear the progress line before returning.
				fmt.Printf("\r%-80s\r", "")
				return
			case now := <-ticker.C:
				print(now)
			}
		}
	}()

	return func() {
		close(done)
		<-stopped
	}
}

// printRunSummary prints a formatted text table for a single run.
func printRunSummary(s RunSummary, cfg *Config) {
	if cfg.JSONOutput {
		return // JSON is handled in bulk at the end
	}

	fmt.Printf("\n=== Run %d ===\n", s.RunNumber)
	fmt.Printf("  Object:       s3://%s/%s\n", cfg.Bucket, cfg.Key)
	fmt.Printf("  Object size:  %s\n", formatBytes(s.ObjectSize))
	fmt.Printf("  Chunk size:   %s  (%d chunks)\n", formatBytes(s.ChunkSize), s.ChunkCount)
	fmt.Printf("  Concurrency:  %d workers\n\n", s.Concurrency)

	fmt.Printf("  Results:\n")
	fmt.Printf("    Total time:        %s\n", formatDuration(s.TotalTime))
	fmt.Printf("    Total bytes:       %s\n", formatBytes(s.TotalBytes))
	fmt.Printf("    Throughput:        %.1f MB/s  (%.3f GB/s)\n", s.ThroughputMB, s.ThroughputGB)
	fmt.Printf("    Time to 1st byte:  %s\n\n", formatDuration(s.TTFB))

	fmt.Printf("  Chunk latency (per-chunk download time):\n")
	fmt.Printf("    Min:   %s\n", formatDuration(s.ChunkLatency.Min))
	fmt.Printf("    Max:   %s\n", formatDuration(s.ChunkLatency.Max))
	fmt.Printf("    Mean:  %s\n", formatDuration(s.ChunkLatency.Mean))
	fmt.Printf("    P50:   %s\n", formatDuration(s.ChunkLatency.P50))
	fmt.Printf("    P95:   %s\n", formatDuration(s.ChunkLatency.P95))
	fmt.Printf("    P99:   %s\n", formatDuration(s.ChunkLatency.P99))
}

// printAggregateSummary prints throughput statistics across all runs for one concurrency level.
func printAggregateSummary(summaries []RunSummary, cfg *Config) {
	agg := computeAggregate(summaries)
	fmt.Printf("\n  Aggregate (%d runs):\n", agg.Runs)
	fmt.Printf("    Throughput  Min:   %.1f MB/s  (%.3f GB/s)\n", agg.MinThroughputMB, agg.MinThroughputGB)
	fmt.Printf("    Throughput  Max:   %.1f MB/s  (%.3f GB/s)\n", agg.MaxThroughputMB, agg.MaxThroughputGB)
	fmt.Printf("    Throughput  Mean:  %.1f MB/s  (%.3f GB/s)\n", agg.MeanThroughputMB, agg.MeanThroughputGB)
}

// printJSONSweeps emits all sweep results as JSON.
func printJSONSweeps(sweeps []ConcurrencySweep) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sweeps); err != nil {
		fmt.Fprintf(os.Stderr, "JSON encode error: %v\n", err)
	}
}

// printComparisonReport prints a summary table and ASCII bar chart comparing all concurrency levels.
func printComparisonReport(sweeps []ConcurrencySweep) {
	fmt.Printf("\n╔══════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║              Concurrency Sweep Comparison               ║\n")
	fmt.Printf("╚══════════════════════════════════════════════════════════╝\n\n")

	// Find best mean throughput for highlighting and bar scaling.
	bestMean := 0.0
	bestIdx := 0
	for i, sw := range sweeps {
		if sw.Aggregate.MeanThroughputMB > bestMean {
			bestMean = sw.Aggregate.MeanThroughputMB
			bestIdx = i
		}
	}

	// Table header.
	fmt.Printf("  %-10s  %5s  %10s  %10s  %10s\n",
		"Workers", "Runs", "Min MB/s", "Mean MB/s", "Max MB/s")
	fmt.Printf("  %-10s  %5s  %10s  %10s  %10s\n",
		"-------", "----", "--------", "---------", "--------")

	for i, sw := range sweeps {
		agg := sw.Aggregate
		best := ""
		if i == bestIdx {
			best = " <-- best"
		}
		fmt.Printf("  %-10d  %5d  %10.1f  %10.1f  %10.1f%s\n",
			sw.Concurrency, agg.Runs,
			agg.MinThroughputMB, agg.MeanThroughputMB, agg.MaxThroughputMB,
			best)
	}

	// ASCII bar chart of mean throughput.
	const barWidth = 40
	fmt.Printf("\n  Mean throughput (MB/s):\n\n")
	for i, sw := range sweeps {
		mean := sw.Aggregate.MeanThroughputMB
		bar := int(mean / bestMean * barWidth)
		if bar < 1 {
			bar = 1
		}
		marker := ""
		if i == bestIdx {
			marker = " (best)"
		}
		fmt.Printf("  %6d workers │%s%s %.1f%s\n",
			sw.Concurrency,
			repeatChar('█', bar),
			repeatChar('░', barWidth-bar),
			mean,
			marker)
	}

	best := sweeps[bestIdx]
	fmt.Printf("\n  Best: %d workers → %.1f MB/s mean  (%.3f GB/s)\n",
		best.Concurrency,
		best.Aggregate.MeanThroughputMB,
		best.Aggregate.MeanThroughputGB)
}

func repeatChar(ch rune, n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]rune, n)
	for i := range buf {
		buf[i] = ch
	}
	return string(buf)
}

// formatBytes returns a human-readable byte size string.
func formatBytes(n int64) string {
	switch {
	case n >= 1<<40:
		return fmt.Sprintf("%.2f TB", float64(n)/(1<<40))
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.2f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// formatDuration returns a concise, human-readable duration string.
func formatDuration(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.3f s", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%.1f ms", float64(d)/float64(time.Millisecond))
	case d >= time.Microsecond:
		return fmt.Sprintf("%.1f µs", float64(d)/float64(time.Microsecond))
	default:
		return fmt.Sprintf("%d ns", d.Nanoseconds())
	}
}
