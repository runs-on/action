# runs-on/action

RunsOn Action for magic caching, and more. This action is required if you are using the magic caching feature of [RunsOn](https://runs-on.com) (`extras=s3-cache` job label).

## Usage

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v1
      - other steps
```

## Options

### `show_env`

Show all environment variables available to actions (used for debugging purposes).

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v1
        with:
          show_env: true
```

### `show_costs`

Displays how much it cost to run that workflow job. Uses https://ec2-pricing.runs-on.com to get accurate data, for both on-demand and spot pricing across all regions and availability zones.

Beta: also compares with similar machine on GitHub.

Example output in the post-step:

```
| metric                 | value           |
| ---------------------- | --------------- |
| Instance Type          | m7i-flex.large  |
| Instance Lifecycle     | on-demand       |
| Region                 | us-east-1       |
| Duration               | 2.06 minutes    |
| Cost                   | $0.0040         |
| GitHub equivalent cost | $0.0240         |
| Savings                | $0.0200 (82.8%) |
```

### `metrics`

Send additional metrics using CloudWatch agent.

Supported metrics:

| Metric Type | Available Metrics |
|------------|------------------|
| `cpu` | `usage_user`, `usage_system` |
| `network` | `bytes_recv`, `bytes_sent` |
| `memory` | `used_percent` |
| `disk` | `used_percent`, `inodes_used` |
| `io` | `io_time`, `reads`, `writes` |

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v1
        with:
          metrics: cpu,network,memory,disk,io
```

The action will display live metrics with sparklines and charts in the post-execution summary.


### `sccache`

Only available for Linux runners.

Configures [`sccache`](https://github.com/mozilla/sccache) so that you can cache the compilation of C/C++ code, Rust, as well as NVIDIA's CUDA.

The only parameter it can take for now is `s3`, which will auto-configure the [S3 cache backend for sccache](https://github.com/mozilla/sccache/blob/main/docs/S3.md), using the RunsOn S3 cache bucket that comes for free (with crazy speed and unlimited storage) with your RunsOn installation.

Example:

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v1
        with:
          sccache: s3
      - uses: mozilla-actions/sccache-action@v0.0.9
      - run: # your slow rust compilation
```

What this does under the hood is the equivalent of:

```bash
echo "SCCACHE_GHA_ENABLED=false" >> $GITHUB_ENV
echo "SCCACHE_BUCKET=${{ env.RUNS_ON_S3_BUCKET_CACHE}}" >> $GITHUB_ENV
echo "SCCACHE_REGION=${{ env.RUNS_ON_AWS_REGION}}" >> $GITHUB_ENV
echo "SCCACHE_S3_KEY_PREFIX=cache/sccache" >> $GITHUB_ENV
echo "RUSTC_WRAPPER=sccache" >> $GITHUB_ENV
```

## Future work

This action will probably host a few other features such as:

- enabling/disabling SSM agent ?