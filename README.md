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

### show-env

Show all environment variables available to actions (used for debugging purposes).

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v1
        with:
          show-env: true
```

### metrics

Send additional metrics using CloudWatch agent.

Supported metrics:

- `memory` (mem_used_percent, mem_available_percent, mem_total, mem_used)
- `disk` (used_percent, free, total, used for all filesystems, excluding sysfs/devtmpfs)  
- `io` (reads, writes, read_bytes, write_bytes, read_time, write_time, io_time)

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v1
        with:
          metrics: memory,disk,io
```

**Note:** AWS provides CPU, network, and basic EBS metrics by default. Memory and detailed disk usage require the CloudWatch agent.

The action will display live metrics with sparklines and charts in the post-execution summary, showing data from the last 6 hours.

## Future work

This action will probably host a few other features such as:

- enabling instance monitoring through CloudWatch (RAM, CPU, etc.)
- enabling/disabling SSM agent ?