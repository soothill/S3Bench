package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func main() {
	cfg, err := parseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n\n", err)
		flag.Usage()
		os.Exit(1)
	}

	ctx := context.Background()

	client, err := buildS3Client(ctx, cfg)
	if err != nil {
		log.Fatalf("building S3 client: %v", err)
	}

	// Discover object size once before timed runs.
	objectSize, err := getObjectSize(ctx, client, cfg.Bucket, cfg.Key)
	if err != nil {
		log.Fatalf("cannot determine object size: %v", err)
	}

	chunks := planChunks(objectSize, cfg.ChunkSize)

	if !cfg.JSONOutput {
		fmt.Printf("s3bench\n")
		fmt.Printf("  Endpoint:    %s\n", endpointDisplay(cfg))
		fmt.Printf("  Object:      s3://%s/%s\n", cfg.Bucket, cfg.Key)
		fmt.Printf("  Object size: %s\n", formatBytes(objectSize))
		fmt.Printf("  Chunk size:  %s  (%d chunks)\n", formatBytes(cfg.ChunkSize), len(chunks))
		fmt.Printf("  Concurrency: %s\n", formatConcurrencyList(cfg.ConcurrencyList))
		fmt.Printf("  Runs:        %d per concurrency level\n", cfg.Runs)
		if cfg.DiscardOutput {
			fmt.Printf("  Output:      discard\n")
		} else {
			fmt.Printf("  Output:      %s\n", cfg.OutputFile)
		}
	}

	// Prepare output file if writing is requested.
	var outFile *os.File
	if !cfg.DiscardOutput && cfg.OutputFile != "" {
		outFile, err = os.Create(cfg.OutputFile)
		if err != nil {
			log.Fatalf("creating output file %q: %v", cfg.OutputFile, err)
		}
		defer outFile.Close()
	}

	var sweeps []ConcurrencySweep
	var progress atomic.Int64
	multiConc := len(cfg.ConcurrencyList) > 1

	for _, conc := range cfg.ConcurrencyList {
		if !cfg.JSONOutput {
			if multiConc {
				fmt.Printf("\n=== Concurrency: %d workers ===\n", conc)
			}
		}

		// Re-plan chunks: chunk count doesn't change with concurrency,
		// but we keep this here for clarity when concurrency > chunk count.
		var runSummaries []RunSummary

		for run := 1; run <= cfg.Runs; run++ {
			// Allocate output buffers only when we need to write the result.
			var outBufs [][]byte
			if !cfg.DiscardOutput {
				outBufs = make([][]byte, len(chunks))
			}

			progress.Store(0)

			var stopProgress func()
			if !cfg.JSONOutput {
				if cfg.Runs > 1 {
					fmt.Printf("\nRun %d/%d\n", run, cfg.Runs)
				}
				stopProgress = startProgressReporter(objectSize, &progress)
			}

			result, err := downloadObject(ctx, client, cfg, chunks, outBufs, &progress, conc)

			if stopProgress != nil {
				stopProgress()
			}

			if err != nil {
				log.Fatalf("concurrency=%d run %d failed: %v", conc, run, err)
			}

			summary := computeStats(result, cfg, objectSize, run, conc)
			runSummaries = append(runSummaries, summary)

			// Flush chunks in order to the output file.
			if outFile != nil {
				for _, buf := range outBufs {
					if _, err := outFile.Write(buf); err != nil {
						log.Fatalf("writing output file: %v", err)
					}
				}
				if run < cfg.Runs {
					if _, err := outFile.Seek(0, io.SeekStart); err != nil {
						log.Fatalf("seeking output file: %v", err)
					}
				}
			}

			if !cfg.JSONOutput {
				printRunSummary(summary, cfg)
			}
		}

		agg := computeAggregate(runSummaries)
		sweeps = append(sweeps, ConcurrencySweep{
			Concurrency: conc,
			Summaries:   runSummaries,
			Aggregate:   agg,
		})

		if !cfg.JSONOutput && cfg.Runs > 1 {
			printAggregateSummary(runSummaries, cfg)
		}
	}

	if cfg.JSONOutput {
		printJSONSweeps(sweeps)
	} else if multiConc {
		printComparisonReport(sweeps)
	}
}

// buildS3Client constructs an S3 client from the program configuration.
func buildS3Client(ctx context.Context, cfg *Config) (*s3.Client, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}

	// Load credentials from the named AWS profile.
	if cfg.Profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(cfg.Profile))
	}

	// Explicit key/secret flags override the profile when both are provided.
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				cfg.AccessKeyID,
				cfg.SecretAccessKey,
				"", // no session token
			),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}

	s3Opts := []func(*s3.Options){}
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			// Path-style addressing is required for MinIO, Ceph, and most
			// non-AWS S3-compatible endpoints.
			o.UsePathStyle = true
		})
	}

	return s3.NewFromConfig(awsCfg, s3Opts...), nil
}

func endpointDisplay(cfg *Config) string {
	if cfg.Endpoint != "" {
		return cfg.Endpoint
	}
	return "AWS S3"
}

func formatConcurrencyList(list []int) string {
	if len(list) == 1 {
		return fmt.Sprintf("%d workers", list[0])
	}
	parts := make([]string, len(list))
	for i, v := range list {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return strings.Join(parts, ", ") + " workers (sweep)"
}
