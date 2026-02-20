// Copyright (c) 2026 Darren Soothill <darren [at] soothill [dot] com>
// All rights reserved.
// Use of this source code is governed by the MIT License.

package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// countingReader wraps an io.Reader and increments a counter as bytes are read.
// This gives live byte-level progress even within a single large chunk.
type countingReader struct {
	r       io.Reader
	counter *atomic.Int64
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 {
		cr.counter.Add(int64(n))
	}
	return n, err
}

// ChunkSpec describes a single byte-range segment of the object.
type ChunkSpec struct {
	Index      int
	RangeStart int64
	RangeEnd   int64 // inclusive, per RFC 7233
	Size       int64 // RangeEnd - RangeStart + 1
}

// ChunkResult holds the timing and outcome of one chunk download.
type ChunkResult struct {
	Index        int
	Size         int64
	StartTime    time.Time
	TTFB         time.Duration // time from GetObject call to response headers received
	ElapsedTotal time.Duration // full elapsed time including body drain
	Err          error
}

// DownloadResult aggregates all chunk results for a single run.
type DownloadResult struct {
	Chunks    []ChunkResult
	TotalTime time.Duration
	TTFB      time.Duration // TTFB of the first chunk to respond
}

// getObjectSize performs a HeadObject to determine the content length of the target object.
func getObjectSize(ctx context.Context, client *s3.Client, bucket, key string) (int64, error) {
	resp, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return 0, fmt.Errorf("HeadObject failed: %w", err)
	}
	if resp.ContentLength == nil {
		return 0, fmt.Errorf("HeadObject returned nil ContentLength — endpoint may not support it")
	}
	return *resp.ContentLength, nil
}

// planChunks divides objectSize into chunks of at most chunkSize bytes.
// The last chunk will be smaller if objectSize is not evenly divisible.
func planChunks(objectSize, chunkSize int64) []ChunkSpec {
	var chunks []ChunkSpec
	index := 0
	for offset := int64(0); offset < objectSize; offset += chunkSize {
		end := offset + chunkSize - 1
		if end >= objectSize {
			end = objectSize - 1
		}
		chunks = append(chunks, ChunkSpec{
			Index:      index,
			RangeStart: offset,
			RangeEnd:   end,
			Size:       end - offset + 1,
		})
		index++
	}
	return chunks
}

// downloadObject downloads all chunks concurrently using a fixed-size worker pool.
// outBufs must be len(chunks) if writing is enabled, or nil for discard mode.
// progress, if non-nil, is incremented as bytes are received (for live display).
func downloadObject(
	ctx context.Context,
	client *s3.Client,
	cfg *Config,
	chunks []ChunkSpec,
	outBufs [][]byte,
	progress *atomic.Int64,
	concurrency int,
) (DownloadResult, error) {

	jobs := make(chan ChunkSpec, len(chunks))
	for _, c := range chunks {
		jobs <- c
	}
	close(jobs)

	results := make([]ChunkResult, len(chunks))

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		firstTTFB time.Duration
		ttfbSet   bool
	)

	overallStart := time.Now()

	workers := concurrency
	if workers > len(chunks) {
		workers = len(chunks)
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for chunk := range jobs {
				res := downloadChunk(ctx, client, cfg, chunk, outBufs, progress)
				// Index-keyed write — no lock needed; each goroutine owns a unique index.
				results[chunk.Index] = res

				if res.Err == nil {
					mu.Lock()
					if !ttfbSet {
						firstTTFB = res.TTFB
						ttfbSet = true
					}
					mu.Unlock()
				}
			}
		}()
	}

	wg.Wait()
	totalTime := time.Since(overallStart)

	for _, r := range results {
		if r.Err != nil {
			return DownloadResult{}, r.Err
		}
	}

	return DownloadResult{
		Chunks:    results,
		TotalTime: totalTime,
		TTFB:      firstTTFB,
	}, nil
}

// downloadChunk performs a single byte-range GetObject request and records timing.
func downloadChunk(
	ctx context.Context,
	client *s3.Client,
	cfg *Config,
	chunk ChunkSpec,
	outBufs [][]byte,
	progress *atomic.Int64,
) ChunkResult {

	rangeHeader := fmt.Sprintf("bytes=%d-%d", chunk.RangeStart, chunk.RangeEnd)
	start := time.Now()

	resp, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(cfg.Bucket),
		Key:    aws.String(cfg.Key),
		Range:  aws.String(rangeHeader),
	})
	if err != nil {
		return ChunkResult{
			Index:     chunk.Index,
			StartTime: start,
			Err:       fmt.Errorf("GetObject chunk %d (range %s): %w", chunk.Index, rangeHeader, err),
		}
	}
	defer resp.Body.Close()

	// TTFB: time elapsed from request dispatch to response headers received.
	// The SDK returns after receiving headers; body bytes are not yet consumed.
	ttfb := time.Since(start)

	// Wrap the body so bytes are counted as they flow through, giving
	// live progress even within a single large chunk.
	var body io.Reader = resp.Body
	if progress != nil {
		body = &countingReader{r: resp.Body, counter: progress}
	}

	var n int64
	if outBufs != nil {
		// Write mode: allocate exactly chunk.Size bytes and fill from body.
		buf := make([]byte, chunk.Size)
		var readN int
		readN, err = io.ReadFull(body, buf)
		n = int64(readN)
		if err != nil && err != io.ErrUnexpectedEOF {
			return ChunkResult{
				Index:     chunk.Index,
				StartTime: start,
				TTFB:      ttfb,
				Err:       fmt.Errorf("reading chunk %d body: %w", chunk.Index, err),
			}
		}
		outBufs[chunk.Index] = buf[:n]
	} else {
		// Discard mode: drain body without allocating an output buffer.
		n, err = io.Copy(io.Discard, body)
		if err != nil {
			return ChunkResult{
				Index:     chunk.Index,
				StartTime: start,
				TTFB:      ttfb,
				Err:       fmt.Errorf("draining chunk %d body: %w", chunk.Index, err),
			}
		}
	}

	return ChunkResult{
		Index:        chunk.Index,
		Size:         n,
		StartTime:    start,
		TTFB:         ttfb,
		ElapsedTotal: time.Since(start),
	}
}
