// Copyright (c) 2026 Darren Soothill <darren [at] soothill [dot] com>
// All rights reserved.
// Use of this source code is governed by the MIT License.

package main

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
)

// Config holds all runtime configuration parsed from CLI flags.
type Config struct {
	Endpoint        string
	Bucket          string
	Key             string
	Region          string
	Profile         string
	AccessKeyID     string
	SecretAccessKey string
	ChunkSize       int64
	ConcurrencyList []int
	Runs            int
	DiscardOutput   bool
	OutputFile      string
	JSONOutput      bool
}

func parseConfig() (*Config, error) {
	var rawChunkSize string
	cfg := &Config{}

	flag.StringVar(&cfg.Endpoint, "endpoint", "", "S3-compatible endpoint URL (empty = AWS)")
	flag.StringVar(&cfg.Bucket, "bucket", "", "S3 bucket name (required)")
	flag.StringVar(&cfg.Key, "key", "", "S3 object key (required)")
	flag.StringVar(&cfg.Region, "region", "us-east-1", "AWS region")
	flag.StringVar(&cfg.Profile, "profile", "impossible", "AWS named profile from ~/.aws/credentials or ~/.aws/config")
	flag.StringVar(&cfg.AccessKeyID, "access-key-id", "", "AWS access key ID (overrides profile)")
	flag.StringVar(&cfg.SecretAccessKey, "secret-access-key", "", "AWS secret access key (overrides profile)")
	flag.StringVar(&rawChunkSize, "chunk-size", "64MB", "Chunk size for byte-range reads (e.g. 64MB, 1GB) or named preset: XS=1MB S=4MB M=8MB L=64MB XL=256MB XXL=1GB")
	var rawConcurrency string
	flag.StringVar(&rawConcurrency, "concurrency", "8", "Parallel download workers — single value or comma-separated list for a sweep (e.g. 8 or 8,16,32)")
	flag.IntVar(&cfg.Runs, "runs", 1, "Number of benchmark runs")
	flag.BoolVar(&cfg.DiscardOutput, "discard", false, "Discard downloaded bytes (benchmark mode, no file write)")
	flag.StringVar(&cfg.OutputFile, "output", "", "Write downloaded object to this file path")
	flag.BoolVar(&cfg.JSONOutput, "json", false, "Emit results as JSON")
	flag.Parse()

	if cfg.Bucket == "" {
		return nil, fmt.Errorf("--bucket is required")
	}
	if cfg.Key == "" {
		return nil, fmt.Errorf("--key is required")
	}
	for _, part := range strings.Split(rawConcurrency, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("--concurrency: invalid value %q (must be a positive integer)", part)
		}
		cfg.ConcurrencyList = append(cfg.ConcurrencyList, n)
	}
	if len(cfg.ConcurrencyList) == 0 {
		return nil, fmt.Errorf("--concurrency must have at least one value")
	}
	if cfg.Runs < 1 {
		return nil, fmt.Errorf("--runs must be >= 1")
	}
	if cfg.DiscardOutput && cfg.OutputFile != "" {
		return nil, fmt.Errorf("--discard and --output are mutually exclusive")
	}
	if !cfg.DiscardOutput && cfg.OutputFile == "" {
		// Default to discard if neither is specified
		cfg.DiscardOutput = true
	}

	var err error
	cfg.ChunkSize, err = parseByteSize(rawChunkSize)
	if err != nil {
		return nil, fmt.Errorf("--chunk-size: %w", err)
	}
	if cfg.ChunkSize < 1 {
		return nil, fmt.Errorf("--chunk-size must be > 0")
	}

	return cfg, nil
}

// namedSizes maps single-word preset names to their byte values.
// These are checked before numeric parsing so bare letters like "M" are unambiguous.
var namedSizes = map[string]int64{
	"XS":  1 << 20,        //   1 MB
	"S":   4 << 20,        //   4 MB
	"M":   8 << 20,        //   8 MB
	"L":   64 << 20,       //  64 MB
	"XL":  256 << 20,      // 256 MB
	"XXL": 1 << 30,        //   1 GB
}

// parseByteSize parses human-friendly byte size strings like "64MB", "1GiB", "512KB",
// or a named preset: XS, S, M, L, XL, XXL.
// Both SI (MB = 1024^2) and IEC (MiB = 1024^2) suffixes are treated as 1024-based.
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty value")
	}

	// Check named presets first (case-insensitive).
	if v, ok := namedSizes[strings.ToUpper(s)]; ok {
		return v, nil
	}

	suffixMap := map[string]int64{
		"B":   1,
		"KB":  1 << 10,
		"KIB": 1 << 10,
		"MB":  1 << 20,
		"MIB": 1 << 20,
		"GB":  1 << 30,
		"GIB": 1 << 30,
		"TB":  1 << 40,
		"TIB": 1 << 40,
	}

	upper := strings.ToUpper(s)
	var suffix string
	var numStr string

	for k := range suffixMap {
		if strings.HasSuffix(upper, k) {
			// Prefer the longest matching suffix
			if len(k) > len(suffix) {
				suffix = k
				numStr = strings.TrimSpace(s[:len(s)-len(k)])
			}
		}
	}

	if suffix == "" {
		// No suffix — treat as raw bytes
		numStr = s
		suffix = "B"
	}

	if numStr == "" {
		return 0, fmt.Errorf("no numeric value in %q", s)
	}

	var value float64
	if _, err := fmt.Sscanf(numStr, "%f", &value); err != nil {
		return 0, fmt.Errorf("cannot parse number %q in %q", numStr, s)
	}
	if value <= 0 {
		return 0, fmt.Errorf("value must be positive in %q", s)
	}

	multiplier := suffixMap[suffix]
	return int64(value * float64(multiplier)), nil
}
