# s3bench

A command-line tool for benchmarking download throughput from AWS S3 and S3-compatible storage (MinIO, Ceph, etc.).

It downloads a single large object by splitting it into configurable byte-range chunks and fetching them concurrently, then reports detailed throughput and latency statistics. A concurrency sweep mode runs the benchmark at multiple concurrency levels automatically and produces a comparison report.

## How it works

1. Issues a `HeadObject` request to determine the object size.
2. Divides the object into chunks of `--chunk-size` bytes (the last chunk covers any remainder).
3. Dispatches up to `--concurrency` goroutines, each downloading its chunk via an HTTP `Range` header (`bytes=<start>-<end>`).
4. Displays a live transfer-rate line while downloading (updated every 200 ms).
5. After all chunks complete, reports throughput, time-to-first-byte, and per-chunk latency percentiles.
6. If multiple concurrency values are given, repeats for each and prints a side-by-side comparison table with an ASCII bar chart.

## Building

Requires [Go 1.22+](https://go.dev/dl/).

```bash
cd S3Bench
go mod tidy        # fetch and pin dependencies
go build -o s3bench .
```

## Credentials

By default the tool loads credentials from the AWS named profile **`impossible`** in `~/.aws/credentials` or `~/.aws/config`. Use `--profile` to select a different profile, or pass explicit keys with `--access-key-id` / `--secret-access-key`.

If no profile exists and no explicit keys are provided, the AWS SDK falls back to its standard chain: environment variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`) → `~/.aws/credentials` → IAM instance role.

### Example `~/.aws/credentials` entry

```ini
[impossible]
aws_access_key_id     = AKIAIOSFODNN7EXAMPLE
aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--bucket` | *(required)* | S3 bucket name |
| `--key` | *(required)* | S3 object key to download |
| `--chunk-size` | `64MB` | Size of each byte-range read. Accepts explicit sizes (`64MB`, `1GB`) or named presets (see below) |
| `--concurrency` | `8` | Parallel download workers. Single value (`16`) or comma-separated list for a sweep (`8,16,32,64`) |
| `--runs` | `1` | Number of times to repeat the benchmark at each concurrency level |
| `--profile` | `impossible` | AWS named profile from `~/.aws/credentials` or `~/.aws/config` |
| `--access-key-id` | `""` | AWS access key ID — overrides `--profile` when both are set |
| `--secret-access-key` | `""` | AWS secret access key — overrides `--profile` when both are set |
| `--region` | `us-east-1` | AWS region |
| `--endpoint` | `""` | Custom S3-compatible endpoint URL (e.g. `http://minio.local:9000`). Enables path-style addressing automatically |
| `--discard` | `false` | Discard downloaded bytes — no file is written. Ideal for pure throughput benchmarking |
| `--output` | `""` | Write the downloaded object to this file path. Mutually exclusive with `--discard` |
| `--json` | `false` | Emit results as JSON instead of a text table |

If neither `--discard` nor `--output` is specified, the tool defaults to discard mode.

## Chunk size presets

In addition to explicit sizes like `64MB` or `1.5GB`, the following named presets are accepted (case-insensitive):

| Preset | Size |
|---|---|
| `XS` | 1 MB |
| `S` | 4 MB |
| `M` | 8 MB |
| `L` | 64 MB |
| `XL` | 256 MB |
| `XXL` | 1 GB |

```bash
./s3bench --chunk-size M   --bucket mybucket --key bigfile.bin --discard
./s3bench --chunk-size xl  --bucket mybucket --key bigfile.bin --discard
./s3bench --chunk-size 128MB --bucket mybucket --key bigfile.bin --discard
```

## Usage examples

### Basic benchmark against AWS S3

```bash
./s3bench \
  --bucket my-bucket \
  --key path/to/large-file.bin \
  --chunk-size 64MB \
  --concurrency 16 \
  --discard
```

### Use a specific AWS profile

```bash
./s3bench \
  --profile my-profile \
  --bucket my-bucket \
  --key path/to/large-file.bin \
  --discard
```

### Multiple runs to get stable averages

```bash
./s3bench \
  --bucket my-bucket \
  --key path/to/large-file.bin \
  --chunk-size L \
  --concurrency 32 \
  --runs 5 \
  --discard
```

### Concurrency sweep — find the optimal worker count

Pass a comma-separated list of concurrency values. The tool runs the full benchmark at each level and prints a comparison report at the end.

```bash
./s3bench \
  --bucket my-bucket \
  --key path/to/large-file.bin \
  --chunk-size 64MB \
  --concurrency 4,8,16,32,64 \
  --runs 3 \
  --discard
```

Example comparison output:

```
╔══════════════════════════════════════════════════════════╗
║              Concurrency Sweep Comparison               ║
╚══════════════════════════════════════════════════════════╝

  Workers     Runs    Min MB/s  Mean MB/s   Max MB/s
  -------     ----    --------  ---------   --------
           4     3       412.1      438.7      461.3
           8     3       781.4      823.9      856.2
          16     3      1102.5     1163.8     1201.4
          32     3      1367.9     1401.5     1423.0 <-- best
          64     3      1389.2     1398.1     1412.7

  Mean throughput (MB/s):

       4 workers │████████████░░░░░░░░░░░░░░░░░░░░░░░░░░░░ 438.7
       8 workers │███████████████████████░░░░░░░░░░░░░░░░░ 823.9
      16 workers │█████████████████████████████████░░░░░░░ 1163.8
      32 workers │████████████████████████████████████████ 1401.5 (best)
      64 workers │███████████████████████████████████████░ 1398.1

  Best: 32 workers → 1401.5 MB/s mean  (1.369 GB/s)
```

### Benchmark against MinIO (or any S3-compatible endpoint)

```bash
./s3bench \
  --endpoint http://minio.local:9000 \
  --bucket testbucket \
  --key bigfile.bin \
  --access-key-id minioadmin \
  --secret-access-key minioadmin \
  --region us-east-1 \
  --chunk-size 64MB \
  --concurrency 8,16,32 \
  --runs 3 \
  --discard
```

### Download to a file

```bash
./s3bench \
  --bucket my-bucket \
  --key path/to/large-file.bin \
  --output /tmp/downloaded.bin
```

### JSON output (useful for scripting)

```bash
# Single concurrency level — emits an array with one sweep entry
./s3bench --bucket b --key k --concurrency 16 --discard --json

# Concurrency sweep — emits an array, one entry per concurrency level
./s3bench --bucket b --key k --concurrency 8,16,32 --runs 3 --discard --json

# Extract mean throughput for each concurrency level
./s3bench --bucket b --key k --concurrency 8,16,32 --discard --json \
  | jq '.[] | {workers: .Concurrency, mean_mb_s: .Aggregate.mean_throughput_mb_s}'
```

## Output

### Live progress line

While each run is in progress, a single line updates in place showing bytes received, percentage complete, current transfer rate, and elapsed time:

```
  2.34 GB / 10.00 GB  ( 23.4%)    1243.7 MB/s   elapsed: 1.9s
```

The rate shown is instantaneous (averaged over the last 200 ms interval), not the overall average.

### Per-run text summary

```
=== Run 1 ===
  Object:       s3://my-bucket/path/to/large-file.bin
  Object size:  10.00 GB
  Chunk size:   64.00 MB  (160 chunks)
  Concurrency:  16 workers

  Results:
    Total time:        8.432 s
    Total bytes:       10.00 GB
    Throughput:        1184.3 MB/s  (1.157 GB/s)
    Time to 1st byte:  42.3 ms

  Chunk latency (per-chunk download time):
    Min:   341.2 ms
    Max:   892.7 ms
    Mean:  526.4 ms
    P50:   512.1 ms
    P95:   781.3 ms
    P99:   856.4 ms
```

When `--runs > 1`, an aggregate summary (min/max/mean throughput across all runs) is printed after each concurrency level.

### JSON (`--json`)

JSON output is always an array of sweep objects, one per concurrency value. Each sweep contains the individual run results and an aggregate:

```json
[
  {
    "Concurrency": 16,
    "Summaries": [
      {
        "run": 1,
        "object_size_bytes": 10737418240,
        "total_bytes_downloaded": 10737418240,
        "chunk_count": 160,
        "chunk_size_bytes": 67108864,
        "concurrency": 16,
        "total_time_ms": 8432000000,
        "ttfb_ms": 42300000,
        "throughput_mb_s": 1184.3,
        "throughput_gb_s": 1.157,
        "chunk_latency": {
          "min_ms": 341200000,
          "max_ms": 892700000,
          "mean_ms": 526400000,
          "p50_ms": 512100000,
          "p95_ms": 781300000,
          "p99_ms": 856400000
        }
      }
    ],
    "Aggregate": {
      "runs": 1,
      "min_throughput_mb_s": 1184.3,
      "max_throughput_mb_s": 1184.3,
      "mean_throughput_mb_s": 1184.3,
      "min_throughput_gb_s": 1.157,
      "max_throughput_gb_s": 1.157,
      "mean_throughput_gb_s": 1.157
    }
  }
]
```

## Tuning tips

- **Use the concurrency sweep**: run `--concurrency 4,8,16,32,64` to automatically find the worker count that saturates your link. Throughput will plateau when you've hit the network or storage ceiling.
- **Chunk size vs concurrency**: more workers with smaller chunks increases connection overhead; fewer larger chunks can leave workers idle. A good starting point is `--chunk-size L --concurrency 16` and sweep from there.
- **Warm vs cold cache**: use `--runs 3` or more and compare mean, not just the first run. The first run is often slower due to cold caches on the storage side.
- **Discard mode**: always use `--discard` when measuring network/storage throughput so local disk I/O does not become the bottleneck.
- **Run from the right place**: results are only meaningful when measured from where your workload actually runs (e.g. an EC2 instance in the same region as the bucket, not your laptop).
