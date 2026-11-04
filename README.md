# runs-on/action

RunsOn Action for magic caching, and more. This action is required if you are using the magic caching feature of [RunsOn](https://runs-on.com) (`extras=s3-cache` job label).

## Usage

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v2
      - other steps
```

When RunsOn fails a job because AWS is reclaiming its EC2 Spot instance, the
post step skips all of its work (cost report, metrics summary, and sticky cache
shutdown). The runner then has time to report the interruption before the
instance shuts down, and RunsOn discards the sticky disk changes of an
interrupted job anyway.

## Options

### `show_env`

Show all environment variables available to actions (used for debugging purposes).

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v2
        with:
          show_env: true
```

Possible values:

* `true` - Show all environment variables
* `false` - Don't show environment variables (default)

### `show_costs`

Displays how much it cost to run that workflow job, and compares it with a similar GitHub-hosted runner.

**RunsOn v3.4.0 and later.** The RunsOn agent reports the cost itself, at the very end of the job, in the "Complete runner" step. This happens for every job, with or without this action, so `show_costs` only chooses how it is displayed. The estimate:

* covers EC2 (on-demand or spot), the root EBS volume, and any sticky disk, from when the instance starts (or the job starts, on a warm-pool instance) until the job ends;
* uses prices your RunsOn control plane already resolves, so the runner makes no pricing or EC2 API calls and needs no internet access.

Example output in the "Complete runner" step:

```
💰 Estimated cost: $0.0034 (GitHub-hosted: $0.0120)
| Metric                   | Value                                      |
| ------------------------ | ------------------------------------------ |
| Instance type            | c7a.large                                  |
| Instance lifecycle       | spot                                       |
| Region                   | us-east-1                                  |
| Availability zone        | us-east-1b                                 |
| Platform                 | linux/x64, 2 vCPUs                         |
| Billed duration          | 2m45s (boot, job, and 5s for shutdown)     |
| Job duration             | 1m44s                                      |
| EC2                      | $0.0014                                    |
| EBS root volume          | $0.0008                                    |
| EBS sticky disk          | $0.0012                                    |
| Total                    | $0.0034                                    |
| GitHub-hosted equivalent | $0.0120 (job duration rounded up to 2 min) |
| Savings                  | $0.0086 (71.7%)                            |
```

The GitHub-hosted equivalent uses GitHub's published per-minute price for the smallest runner with at least as many vCPUs. It bills only the job duration, rounded up to the next whole minute as GitHub does, since GitHub doesn't bill runner boot.

**Earlier RunsOn versions.** The action's post step computes the cost from https://ec2-pricing.runs-on.com, for both on-demand and spot pricing across all regions and availability zones. It covers EC2 only, up to the post step. When the cost API has no matching pricing data, cost reporting logs an informational message and skips the cost table and job summary. This includes unsupported regions and unavailable instance or zone prices. Other API and network failures still warn.

Possible values:

* `inline` - Display costs in the log output (default)
* `summary` - Display costs in the log output and in the GitHub job summary
* Any other value - Disables the feature

When `runs-on/action` is invoked more than once in the same job, only the first
invocation with cost reporting enabled calculates and displays the job cost.
Later invocations skip duplicate reporting automatically. An invocation with
cost reporting disabled does not prevent a later enabled invocation from
reporting.

### `metrics`

**Note: this is currently only available with a development release of RunsOn. This will be fully functional with v2.8.4+**

Send additional metrics using CloudWatch agent.

Supported metrics:

| Metric Type | Available Metrics |
|------------|------------------|
| `cpu` | `usage_user`, `usage_system` |
| `network` | `bytes_recv`, `bytes_sent` |
| `memory` | `used_percent` |
| `disk` | `used_percent`, `inodes_used`, `free`, `total` |
| `io` | `io_time`, `reads`, `writes` |

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v2
        with:
          metrics: cpu,network,memory,disk,io
```

Possible values:

* `cpu` - CPU usage metrics (`usage_user`, `usage_system`)
* `network` - Network metrics (`bytes_recv`, `bytes_sent`)
* `memory` - Memory metrics (`used_percent`)
* `disk` - Disk metrics (`used_percent`, `inodes_used`, `free`, `total`)
* `io` - I/O metrics (`io_time`, `reads`, `writes`)
* Comma-separated combinations (e.g., `cpu,network,memory,disk,io`)
* Empty string - No additional metrics (default)

Disk metrics are published for each of `/`, `/tmp`, `/var/lib/docker` and `/home/runner` that is a mount point, with the `InstanceId`, `path`, `fstype`, `device` and `VolumeId` dimensions. `VolumeId` is the EBS volume behind that mount, so a sticky disk or snapshot volume mounted at `/var/lib/docker` reports its own volume. Mounts that aren't on an EBS volume have no `VolumeId`, for example `tmpfs`, `overlay`, or the `md0` array RunsOn builds from local instance storage. Earlier versions published disk metrics without the `device` and `VolumeId` dimensions, so update dashboards or alarms that match on the previous set.

The action will display live metrics with charts in the post-execution summary.

```
📈 Metrics (since 2025-06-30T14:18:56Z):


📊 CPU User:
   100.0 ┤
    87.5 ┤                                        ╭─╮╭───────────╮
    75.0 ┤                                       ╭╯ ╰╯           │
    62.5 ┤                                      ╭╯               ╰╮
    50.0 ┤                                      │                 │
    37.5 ┤                                      │                 ╰╮
    25.0 ┤                                     ╭╯                  │
    12.5 ┤                    ╭─────────╮╭─────╯                   ╰╮
     0.0 ┼────────────────────╯         ╰╯                          ╰
                               CPU User (Percent)
  Stats: min:0.0 avg:29.0 max:93.4 Percent


📊 Memory Used:
   100.0 ┤
    87.5 ┤
    75.0 ┤
    62.5 ┤
    50.0 ┤
    37.5 ┤
    25.0 ┤                                             ╭────────╮
    12.5 ┤                            ╭──╮      ╭──────╯        ╰───╮
     0.0 ┼────────────────────────────╯  ╰──────╯                   ╰
                             Memory Used (Percent)
  Stats: min:0.5 avg:7.4 max:20.9 Percent
```

<details>
<summary>Example full output:</summary>

```
📈 Metrics (since 2025-06-30T14:18:56Z):


📊 CPU User:
   100.0 ┤
    87.5 ┤                                        ╭─╮╭───────────╮
    75.0 ┤                                       ╭╯ ╰╯           │
    62.5 ┤                                      ╭╯               ╰╮
    50.0 ┤                                      │                 │
    37.5 ┤                                      │                 ╰╮
    25.0 ┤                                     ╭╯                  │
    12.5 ┤                    ╭─────────╮╭─────╯                   ╰╮
     0.0 ┼────────────────────╯         ╰╯                          ╰
                               CPU User (Percent)
  Stats: min:0.0 avg:29.0 max:93.4 Percent



📊 CPU System:
   100.0 ┤
    87.5 ┤
    75.0 ┤
    62.5 ┤
    50.0 ┤
    37.5 ┤
    25.0 ┤                                     ╭──╮
    12.5 ┤                                    ╭╯  ╰──────────────╮
     0.0 ┼────────────────────────────────────╯                  ╰───
                              CPU System (Percent)
  Stats: min:0.2 avg:5.0 max:33.7 Percent



📊 Memory Used:
   100.0 ┤
    87.5 ┤
    75.0 ┤
    62.5 ┤
    50.0 ┤
    37.5 ┤
    25.0 ┤                                             ╭────────╮
    12.5 ┤                            ╭──╮      ╭──────╯        ╰───╮
     0.0 ┼────────────────────────────╯  ╰──────╯                   ╰
                             Memory Used (Percent)
  Stats: min:0.5 avg:7.4 max:20.9 Percent



📊 Disk Used:
   100.0 ┤
    87.5 ┤
    75.0 ┤            ╭──────────────────────────────────────────────
    62.5 ┤         ╭──╯
    50.0 ┤   ╭─────╯
    37.5 ┼───╯
    25.0 ┤
    12.5 ┤
     0.0 ┤
                              Disk Used (Percent)
  Stats: min:35.6 avg:68.7 max:75.8 Percent



📊 Disk Inodes Used:
   481238 ┤           ╭───────────────────────────────────────────────
   450852 ┤          ╭╯
   420466 ┤          │
   390080 ┤         ╭╯
   359694 ┤         │
   329307 ┤        ╭╯
   298921 ┤       ╭╯
   268535 ┤   ╭───╯
   238149 ┼───╯
                            Disk Inodes Used (Inodes)
  Stats: min:238149.0 avg:440393.1 max:481238.0 Inodes



📊 Disk IO Time:
   10000 ┤             ╭─╮
    8750 ┤   ╭╮       ╭╯ ╰╮
    7500 ┤   ││      ╭╯   │
    6251 ┤   ││      │    │
    5001 ┤  ╭╯╰╮ ╭╮ ╭╯    │
    3751 ┤  │  │ ││ │     ╰╮
    2502 ┤  │  │╭╯╰─╯      │
    1252 ┤ ╭╯  ╰╯          ╰╮                ╭──╮
       2 ┼─╯                ╰────────────────╯  ╰────────────────────
                               Disk IO Time (ms)
  Stats: min:1.0 avg:1581.3 max:10000.0 ms



📊 Disk Reads:
   1472 ┤   ╭╮
   1288 ┤   ││
   1104 ┤   ││
    920 ┤  ╭╯│
    736 ┤  │ ╰╮
    552 ┤  │  │
    368 ┤  │  │         ╭─╮
    184 ┤ ╭╯  ╰╮       ╭╯ ╰─╮
      0 ┼─╯    ╰───────╯    ╰───────────────────────────────────────
                              Disk Reads (Ops/s)
  Stats: min:0.0 avg:81.8 max:1519.0 Ops/s



📊 Disk Writes:
   18816 ┤            ╭─╮
   16465 ┤         ╭──╯ ╰╮
   14113 ┤        ╭╯     ╰╮
   11762 ┤   ╭╮  ╭╯       │
    9411 ┤   ││  │        │
    7059 ┤  ╭╯╰╮╭╯        ╰╮
    4708 ┤  │  ││          │
    2356 ┤ ╭╯  ╰╯          │                ╭───╮
       5 ┼─╯               ╰────────────────╯   ╰────────────────────
                              Disk Writes (Ops/s)
  Stats: min:4.0 avg:3373.4 max:19192.0 Ops/s



📊 Network Received:
   934237025 ┤ ╭╮
   817458485 ┤ ││     ╭─╮
   700679945 ┤ ││    ╭╯ │
   583901406 ┤╭╯│    │  │
   467122866 ┤│ ╰╮   │  │
   350344327 ┤│  │  ╭╯  ╰╮
   233565787 ┼╯  │  │    │
   116787247 ┤   │  │    │
        8708 ┤   ╰──╯    ╰───────────────────────────────────────────────
                                Network Received (Bytes)
  Stats: min:8707.0 avg:91377905.1 max:950344235.0 Bytes



📊 Network Sent:
   1866827 ┼╮
   1634232 ┤│
   1401638 ┤╰╮
   1169043 ┤ │
    936449 ┤ ╰╮
    703854 ┤  │    ╭──╮
    471259 ┤  ╰╮  ╭╯  │
    238665 ┤   │  │   ╰╮                        ╭╮
      6070 ┤   ╰──╯    ╰────────────────────────╯╰─────────────────────
                                Network Sent (Bytes)
  Stats: min:6068.0 avg:159559.6 max:1866827.0 Bytes
```
</details>

### `sccache`

Available on RunsOn Linux and Windows runners.

`sccache: s3` configures [`sccache`](https://github.com/mozilla/sccache), which caches the compilation of C/C++ code, Rust and NVIDIA's CUDA, with its [S3 cache backend](https://github.com/mozilla/sccache/blob/main/docs/S3.md): sccache writes straight to the RunsOn S3 cache bucket, with the runner's instance role. For sccache through the RunsOn agent, with GitHub's branch scoping and cache isolation, use [`remote_cache: sccache`](#remote_cache-sccache) instead. When `remote_cache` also lists `sccache`, it wins, and the action ignores `sccache: s3` with a warning.

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/extras=s3-cache
    env:
      CARGO_INCREMENTAL: "0"
    steps:
      - uses: actions/checkout@v7
      - uses: taiki-e/install-action@v2
        with:
          tool: sccache
      - uses: runs-on/action@v2
        with:
          sccache: s3
      - run: cargo build --release --locked
```

For Rust, disable incremental compilation as shown above. This mode needs no secret, so the action only exports settings, and the first sccache client of a later step, such as Cargo through `RUSTC_WRAPPER`, starts the server, which reads them then. The installer can therefore run before or after this action, but nothing may start the sccache server before it: a running server keeps its startup configuration.

What this does under the hood is the equivalent of:

```bash
echo "SCCACHE_GHA_ENABLED=false" >> $GITHUB_ENV
echo "SCCACHE_BUCKET=${{ env.RUNS_ON_S3_BUCKET_CACHE}}" >> $GITHUB_ENV
echo "SCCACHE_REGION=${{ env.RUNS_ON_AWS_REGION}}" >> $GITHUB_ENV
echo "SCCACHE_S3_KEY_PREFIX=cache/sccache/${{ github.repository_id }}/linux-x64/v1" >> $GITHUB_ENV
echo "RUSTC_WRAPPER=sccache" >> $GITHUB_ENV
```

The action scopes compiler cache objects per repository and per runner platform:

```text
cache/sccache/<repository id>/<runner os>-<runner arch>/v1
```

The repository id is used rather than the `owner/name` slug so that renaming or transferring a repository does not invalidate its cache; the action falls back to the slug (as two key components, `<owner>/<name>`) when `GITHUB_REPOSITORY_ID` is not exposed. The trailing `v1` is a layout version, so a future change to the key layout can be rolled out without reusing existing objects.

This is operational isolation, not a security boundary: repositories sharing a RunsOn stack still share the bucket and the runner IAM role, every branch of a repository reads and writes the same entries, and the stack's cache isolation does not cover them. It provides per-repository cache ownership, growth and cost attribution, targeted invalidation, and freedom to change one repository's cache layout without touching the others.

Previously every repository on a stack shared the flat `cache/sccache` prefix. Moving to the scoped layout starts one cold cache per repository and platform. Objects written under the old prefix are left to the stack's cache lifecycle rule, which expires everything under `cache/` after `S3CacheExpirationInDays` (10 by default).

### `remote_cache`

Gives build tools a remote cache that the RunsOn agent stores in the stack's S3 cache bucket, with GitHub's branch scoping: a pull request reads its base branch's entries and writes only its own, and the stack's cache isolation covers them when it is enabled. Entries are shared between jobs as soon as they are written. List one tool per line, with comma-separated `key=value` options, as in `sticky_cache`:

```yaml
- uses: runs-on/action@v2
  with:
    remote_cache: |
      bazel
      go,tests=true
      gradle
      mbx
      sccache
      turbo
```

| Tool | What the action configures |
| --- | --- |
| `bazel` | `--remote_cache` in Bazel's system rc (Bazel 6.3 or later) |
| `go` | `GOCACHEPROG` (Go 1.24 or later); `go,tests=true` also shares test results |
| `gradle` | an init script for Gradle's HTTP build cache (Gradle 7.6 or later) |
| `mbx` | `MBX_REMOTE_URL` and `MBX_REMOTE_NAMESPACE` |
| `sccache` | `RUSTC_WRAPPER` and `SCCACHE_WEBDAV_ENDPOINT` |
| `turbo` | `TURBO_API`, `TURBO_TOKEN` and `TURBO_TEAM` for Turborepo's remote cache (RunsOn v3.6.0 or later) |

Requires Magic Cache (`extras=s3-cache`) and RunsOn v3.6.0 or later. Otherwise, the action warns and each tool builds without the remote cache. A line with an unknown tool or option is skipped with a warning; the other tools are still configured.

`sticky_cache` keeps a tool's directories on the job's sticky disk, restored when the same lineage runs again. `remote_cache` shares entries through S3 with every job and branch that can read them, while they build.

#### `remote_cache: bazel`

The action writes `--remote_cache` into Bazel's system rc, so every later `bazel` command uses it without a flag, and a project that sets its own `--remote_cache` keeps it. Action results follow GitHub's branch scoping: a job writes them to its own branch or pull request, and reads its base and default branch's first. Outputs are verified against their digest and shared across the repository.

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=8cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v2
        with:
          remote_cache: bazel
      - uses: actions/checkout@v5
      - run: bazel test //... --remote_download_minimal
```

`--remote_download_minimal` skips downloading outputs that later actions do not need, which matters most on warm builds. The rc gets `common --remote_cache=` with the URL the agent gives Bazel's cache (`http://<runner IP>:6871/_runs-on/tools/<id>/bazel`), in `/etc/bazel.bazelrc` (with `sudo`), or `%ProgramData%\bazel.bazelrc` on Windows. In a container job, the action writes the container's system rc, which needs root or passwordless `sudo` in the container; without either, it warns.

#### `remote_cache: go`

Shares the go command's build cache (Go 1.24 or later) between jobs. Later steps get [`GOCACHEPROG`](https://pkg.go.dev/cmd/go#hdr-Build_and_test_caching) pointing at this action's own binary, which keeps the cache on local disk and in the agent's cache. When the job starts, the action downloads in the background the entries the same job used in its previous run, so the go command finds them on local disk instead of fetching them one at a time. Its post step saves the list of what the job used. Put the action first in the job: the download then runs while the next steps check out the code and install Go. A job is its workflow file, its name and its matrix leg, so each leg of a matrix keeps its own list.

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=4cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v2
        with:
          remote_cache: go
      - uses: actions/checkout@v5
      - uses: actions/setup-go@v6
        with:
          go-version: stable
          cache: false # the build cache is in the remote cache; this would save a second copy
      - uses: actions/cache@v5
        with:
          path: ~/go/pkg/mod
          key: gomod-${{ runner.os }}-${{ hashFiles('**/go.sum') }}
      - run: go build ./... && go test ./...
```

Container jobs work too: the runner mounts the action, and so the helper, into the job container. The helper finds the job's key/value cache in `RUNS_ON_REMOTE_CACHE_URL`; the runtime token reaches no run step.

Compiled packages are shared between jobs; test results are not. Within a job, tests whose inputs did not change are still reported as `(cached)`, but a new job runs its tests again, because a test can depend on things Go does not track, such as a database or the network.

To share test results too, use `go,tests=true`. A job then reports `(cached)` for a test that passed in another job with the same inputs: the package, its dependencies, and the files and environment variables the test read through the standard library. Go does not see a database, the network or a file read by a child process, so keep the default for tests that depend on those, or run them with `go test -count=1`. Jobs that use `go` never read the results that `go,tests=true` jobs share. What a passing test printed is stored with its result, so with `tests=true` it is readable by any job that can read the cache, including pull requests from forks: do not use it for tests that print secrets.

#### `remote_cache: gradle`

Gives Gradle a [remote build cache](https://docs.gradle.org/current/userguide/build_cache.html) in the agent's key/value cache. The action writes an init script into Gradle's user home (`$GRADLE_USER_HOME/init.d`; when `GRADLE_USER_HOME` is unset, the action sets it to `~/.gradle` for later steps, since in container jobs Gradle would otherwise take the account's home rather than `HOME`) that points every build of the job at `$RUNS_ON_REMOTE_CACHE_URL/gradle/`. Builds that have the build cache on, with `org.gradle.caching=true` in `gradle.properties` or `--build-cache`, then take the outputs of cacheable tasks, compilations and tests among them, from whatever job ran them first with the same inputs. A build that has it off says so in its output.

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=16cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v2
        with:
          remote_cache: gradle
      - uses: actions/checkout@v5
      - uses: gradle/actions/setup-gradle@v6 # the distribution and dependencies
      - run: ./gradlew build --build-cache
```

The build cache holds task outputs, not Gradle's distribution, dependencies or compiled build scripts, which most of a warm build's time goes to without a cache of its own: keep `gradle/actions/setup-gradle` for those. It only saves on the default branch by default, so on a pull request, a push to the pull request rebuilds what earlier pushes changed; `remote_cache: gradle` saves those outputs in the pull request's scope.

Gradle 7.6 to 9 are tested. A build that configures its own enabled remote cache, for example Develocity's, keeps it, and Gradle says so in its output; a disabled one is replaced. The post step removes the init script.

The init script holds the job's cache URL, which changes from one job to the next, so a [configuration cache](https://docs.gradle.org/current/userguide/configuration_cache.html) restored from another job, as `setup-gradle` does when given a `cache-encryption-key`, is not reused.

#### `remote_cache: sccache`

Points sccache's [WebDAV backend](https://github.com/mozilla/sccache/blob/main/docs/Webdav.md) at the agent's cache. sccache has had a WebDAV storage since 0.4.0; this is tested with 0.18.0.

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/extras=s3-cache
    env:
      CARGO_INCREMENTAL: "0"
    steps:
      - uses: actions/checkout@v7
      - uses: runs-on/action@v2
        with:
          remote_cache: sccache
      - uses: taiki-e/install-action@v2
        with:
          tool: sccache@0.18.0
      - run: cargo build --release --locked
```

The action exports `SCCACHE_WEBDAV_ENDPOINT` with the URL the agent gives sccache's cache (`<Magic Cache URL>/_runs-on/tools/<id>/sccache`), `RUSTC_WRAPPER=sccache` and `SCCACHE_GHA_ENABLED=false`. sccache needs no token: only the agent keeps the job's runtime token.

Install sccache before compiling. Its first client starts the compiler daemon in the job or container. The action never starts or stops it. It can exit and restart from a later step without a new action invocation. Configure the action before starting the daemon; restart an existing daemon yourself after changing its configuration.

Use `SCCACHE_WEBDAV_KEY_PREFIX` for an additional namespace. sccache ranks WebDAV below its S3, Redis, Memcached, GCS, GitHub Actions cache and Azure storages, so a setting for one of them, such as a `SCCACHE_BUCKET` left from `sccache: s3`, or a `SCCACHE_MULTILEVEL_CHAIN` without `webdav`, wins over the agent's cache: the action warns when the environment has one (it does not read sccache's config file, whose storages win too). A job that sets WebDAV credentials (`SCCACHE_WEBDAV_TOKEN`, `SCCACHE_WEBDAV_USERNAME` or `SCCACHE_WEBDAV_PASSWORD`) keeps its own WebDAV server, since sccache would send them to the agent; `SCCACHE_WEBDAV_RW_MODE=READ_ONLY` applies to the agent's cache. For Rust, disable incremental compilation. C and C++ builds can use `cmake -DCMAKE_C_COMPILER_LAUNCHER=sccache -DCMAKE_CXX_COMPILER_LAUNCHER=sccache`.

#### `remote_cache: mbx`

Gives [mbx](https://github.com/jdx/mr-boxington) a remote cache: later steps get `MBX_REMOTE_URL`, in the `mbx` namespace of the agent's key/value cache, and, unless the job sets one, `MBX_REMOTE_NAMESPACE` (the repository's ID). mbx sends its namespace in a header that the agent does not read, so the URL has a path per mbx namespace: set `MBX_REMOTE_NAMESPACE` in the job's `env:`, or before this action, for it to separate entries. Set up mbx after this action, so its setup finds them:

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=8cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v2
        with:
          remote_cache: mbx
      - uses: actions/checkout@v5
      - uses: jdx/mr-boxington-action@v1.7.2
        with:
          backend: remote
      - run: mbx build --workspace --locked
```

The action also stores mbx's capabilities document in the cache, claiming none of the protocol's optional features, which is what mbx builds assume without one; `mbx doctor`, and so mbx's setup action, require it. By default, mbx publishes only from pushes to protected branches, so pull requests read what their base branch published. mbx sends the agent no token: the URL stands for the job's runtime token. mbx refuses to send its own credentials over plain HTTP, so a job that sets `MBX_REMOTE_TOKEN`, `MBX_REMOTE_TOKEN_FILE` or `MBX_REMOTE_OIDC_AUDIENCE` gets a warning, and the action leaves mbx alone. So does a job that sets `MBX_REMOTE_S3_*` settings, which mbx refuses with an HTTP cache.

#### `remote_cache: turbo`

Gives [Turborepo](https://turborepo.dev) a [remote cache](https://turborepo.dev/docs/core-concepts/remote-caching) in the agent, which serves Turborepo's remote cache API. Later steps get `TURBO_API` with the URL the agent gives turbo's cache (`<Magic Cache URL>/_runs-on/tools/<id>/turbo`), and `TURBO_TOKEN` and `TURBO_TEAM` set to `runs-on`: turbo needs both to turn its remote cache on, and the agent ignores them, since the URL stands for the job's runtime token.

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=8cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v2
        with:
          remote_cache: turbo
      - uses: actions/checkout@v5
      - uses: actions/setup-node@v5
        with:
          node-version: 22
          cache: npm
      - run: npm ci
      - run: npx turbo run build test
```

turbo prints `Remote caching enabled` when it uses the cache, and `cache hit, replaying logs` for each task it restores. Task outputs follow GitHub's branch scoping: a job writes them to its own branch or pull request, and reads its own first, then its base and default branch's, so the default branch warms the cache for pull requests. turbo's task hash leaves out the platform, so each runner OS and architecture has its own entries. A hit restores only the files a task lists in `outputs` in `turbo.json`. Signed artifacts (`remoteCache.signature` and `TURBO_REMOTE_CACHE_SIGNATURE_KEY`) work unchanged: the agent stores the signature with the artifact.

A job that sets `TURBO_API`, `TURBO_TOKEN`, `TURBO_TEAM` or `TURBO_TEAMID`, for example for Vercel's remote cache, keeps it: the action warns and leaves turbo alone. When the job's token cannot write to the cache, as in a pull request from a fork, the action also sets `TURBO_CACHE=local:rw,remote:r` unless the job sets `TURBO_CACHE`, so turbo reads the cache without trying to upload: after a refused upload, turbo stops using the remote cache for the rest of its run. `--cache` on turbo's command line wins over it. Tested with Turborepo 2.11; `TURBO_CACHE` needs 2.2 or later. Container jobs reach the agent too.

#### Other clients

Later steps get the agent's key/value cache as `RUNS_ON_REMOTE_CACHE_URL`, so they can point other clients that store entries over plain HTTP at it, in a namespace of their own (1 to 64 lowercase letters, digits, `-`, `_` or `.`): `GET` and `HEAD` on `$RUNS_ON_REMOTE_CACHE_URL/<namespace>/<key>` answer the entry or `404`, and `PUT` replaces it. For example, ccache's HTTP storage:

```yaml
- uses: runs-on/action@v2
  with:
    remote_cache: sccache # or any other tool
- run: |
    echo "CCACHE_REMOTE_STORAGE=$RUNS_ON_REMOTE_CACHE_URL/ccache/" >> "$GITHUB_ENV"
```

The variable holds the same credential as the tools' configuration: whatever reads it can use the job's cache scopes on this runner until the job's token expires.

#### How the job reaches the cache

None of these tools sends a GitHub token, so the action sends the job's runtime token to the agent once (`POST /_runs-on/tools`), and the agent answers with an ID that stands for it, `<id>`, and the path of each protocol it serves: `bazel` (Bazel's HTTP cache), `sccache` (sccache's WebDAV storage) and `kv`, a plain key/value cache that `go`, `gradle`, `mbx` and other clients use with namespaces of their own. The paths hold the ID (`/_runs-on/tools/<id>/kv`), and the action uses them as given, on the same agent. The same token always gets the same paths, nothing without the token can compute them, and they work on this runner only, until the token expires. The action masks `<id>` in the job's logs. It configures a tool only when the agent gives its protocol a path (`GET /_runs-on/tools` lists the protocols without a token); on an older agent, or one without that protocol, the action warns and leaves the tool alone.

### `sticky_cache`

Available for Linux and Windows runners on jobs with a sticky-disk label. Use `sticky=<size>` for the default snapshot lineage or `sticky=<name>:<size>` for a named lineage; the optional name must come first. Volume settings follow the size, for example `sticky=go-cache:20gb:gp3:750mbs:6000iops`. The `apt`, `buildkit`, and `git` cache modes are Linux only.

Persists package manager caches across jobs by bind-mounting them onto the job's sticky disk — a dedicated EBS volume that is snapshotted at job completion and restored (per repo, name, architecture, and branch) on the next job. No tarball upload/download: caches are available at native disk speed, with no size penalty on job duration.

Example:

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/sticky=20gb
    steps:
      - uses: actions/checkout@v7
      - uses: runs-on/action@v2
        with:
          sticky_cache: |
            go
            node
```

Each non-empty line is one cache record. A record starts with a mode and may
include comma-separated `key=value` options. Use one line per mode; the old
comma-separated mode list is not supported.

```yaml
with:
  sticky_cache: |
    go
    node
    buildkit
    custom,path=vendor/custom-cache,path=~/.cache/my-tool
```

On RunsOn runners, the action fails if the sticky disk contract is absent or
the disk does not become ready before `sticky_wait_timeout`. If the runner
reports that the requested disk is unavailable, the action warns and skips all
sticky cache operations so the job can continue cold. On any other runner (for
example a workflow falling back to GitHub-hosted runners), the action skips all
operations and exits successfully, so the same workflow keeps working without
sticky caches. The `custom` mode requires one or more `path=` options;
repeat the record or option to persist several paths. Relative paths resolve
from `GITHUB_WORKSPACE`, `~/` resolves from the runner home, and absolute paths
are preserved. Literal commas in paths are unsupported.

Supported cache modes and the directories they persist:

| Mode | Aliases | Cached paths |
|---|---|---|
| `go` | `golang` | `~/.cache/go-build`, `~/go/pkg/mod` |
| `node` | `npm` | `~/.npm` |
| `yarn` | | `~/.cache/yarn` |
| `pnpm` | | `~/.local/share/pnpm/store` (or `$XDG_DATA_HOME/pnpm/store`) |
| `ruby` | `bundler` | `~/.bundle/cache`, `vendor/bundle` |
| `rust` | `cargo` | `~/.cargo/registry`, `~/.cargo/git` |
| `python` | `pip` | `~/.cache/pip` |
| `uv` | | `~/.cache/uv` |
| `poetry` | | `~/.cache/pypoetry` |
| `apt` | | `/var/cache/apt/archives` |
| `buildkit` | `buildx` | BuildKit layer cache (the official setup-buildx builder stores its state on the sticky disk) |
| `git` | `checkout` | Current workflow repository history (local Git proxy serving the workflow SHA from the sticky disk) |
| `git-full` | | Full Git repository mirrors for workflows that need arbitrary refs or repositories |
| `gradle` | | `~/.gradle/caches`, `~/.gradle/wrapper` |
| `maven` | | `~/.m2/repository` |
| `playwright` | | `~/.cache/ms-playwright` |
| `tool-cache` | | `$RUNNER_TOOL_CACHE` (toolchains installed by `setup-*` actions) |
| `custom` | | One or more paths supplied with `path=` |

#### `tool-cache` mode

The `tool-cache` mode persists toolchains installed through GitHub's tool
cache. Run this action before actions such as `actions/setup-go`,
`actions/setup-node`, or `actions/setup-python`:

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/sticky=tools-ubuntu24:20gb
    steps:
      - uses: actions/checkout@v7
      - uses: runs-on/action@v2
        with:
          sticky_cache: tool-cache
      - uses: actions/setup-go@v7
        with:
          go-version: '1.25.1'
```

This mode mounts an empty or restored sticky directory directly over the
runner-provided `RUNNER_TOOL_CACHE` path, and persists only the toolchains
installed after the mount. It does not copy toolchains from the runner image
into the sticky cache, so the image's preinstalled toolchains are hidden for
the rest of the job: `setup-*` actions download any version they need, the
`GOROOT_*` variables (and, on Linux, the default `go` linked into `/usr/bin`)
point to missing directories, and `github/codeql-action` downloads its CodeQL
bundle. Use it for jobs that install a large toolchain the image does not ship.

It supports Linux and Windows and does not cache package dependencies or build
outputs. Use a sticky-disk name tied to the runner image, as restored binaries
may not be compatible with another operating system image.

#### `buildkit` mode (Docker layer cache)

The `buildkit` mode prepares the state volume used by Docker's official `setup-buildx-action`, backed by the sticky disk. RunsOn does not download or start its own BuildKit daemon. The actions must run in this order, with the fixed builder topology and cleanup settings shown below:

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/sticky=docker:20gb
    steps:
      - uses: actions/checkout@v7
      - id: runs-on
        uses: runs-on/action@v2
        with:
          sticky_cache: buildkit
      - uses: docker/setup-buildx-action@bb05f3f5519dd87d3ba754cc423b652a5edd6d2c # v4
        with:
          name: ${{ steps.runs-on.outputs.buildkit-builder }}
          version: v0.34.1
          driver: docker-container
          driver-opts: |
            image=moby/buildkit:v0.31.1
          cleanup: false
      - uses: docker/build-push-action@v7
        with:
          builder: ${{ steps.runs-on.outputs.buildkit-builder }}
          context: .
          load: true
```

Sticky BuildKit caching supports one `docker-container` node named by the `buildkit-builder` output. The RunsOn post step verifies that setup-buildx mounted the expected sticky volume, then stops and removes the builder before the disk is snapshotted. A missing setup step, reversed action order, different builder name, appended node, or setup-buildx cleanup causes a clear failure instead of silently using ephemeral cache storage.

The action always emits `buildkit-builder`, even without a sticky disk. On
Linux, when the RunsOn `ecr-pull-through` extra has a Docker Hub prefix
configured, the runner agent writes Buildx's standard
`~/.docker/buildx/buildkitd.default.toml` before the job if that file does not
already exist. `docker/setup-buildx-action` discovers that file automatically,
so sticky and regular `docker-container` builders use the prefixed ECR Docker
Hub cache without an action-specific mirror URL or inline configuration.

Use `docker buildx build` (or `docker/build-push-action`) with the emitted builder; add `--load` when you need the built image in the local Docker daemon. `docker pull` and plain `docker build` do not use this cache.

#### `git` mode (fast checkouts)

The `git` mode accelerates `actions/checkout` without changing the checkout step. It starts a local Git proxy whose bare repository lives on the sticky disk, and rewrites `https://github.com/` fetch URLs to it (global `url.insteadOf`). The cache starts with the complete history reachable from `GITHUB_SHA`, so deeper shallow checkouts work without downloading every ref. GitHub still supplies the live ref advertisement. When the workflow repository requests additional branch or tag tips, including with `fetch-depth: 0`, the proxy adds those objects under its private scoped namespace and serves the checkout locally. Secondary repositories and exact SHAs unavailable from current branch or tag tips go upstream unchanged.

Use `git-full` when the workflow must discover or checkout arbitrary refs, or when it repeatedly checks out secondary repositories. This mode preserves full mirror behavior: every requested github.com repository and all of its refs are cached. Its cold download and disk usage can be much larger.

Unlike other modes, the `git` mode must run **before** `actions/checkout`:

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/sticky=20gb
    steps:
      - uses: runs-on/action@v2
        with:
          sticky_cache: git
      - uses: actions/checkout@v7
```

To combine it with any workspace-relative cache, including `ruby` (`vendor/bundle`) or a relative `custom,path=...`, run the action twice: use `git` before checkout, then run the workspace-relative cache after checkout. The second invocation reuses the already-running proxy. Combining them in one invocation fails early so a workspace mount cannot interfere with checkout.

Repeated invocations share the same job-level cost-reporting claim, so the
second invocation does not calculate or print the job cost again.

Notes and limitations:

* Mirror syncs authenticate with the `token` input (default: `${{ github.token }}`). In both Git modes, checking out **other private repositories** requires passing a PAT with access to them, since the rewritten URLs no longer match the credentials configured by `actions/checkout`.
* `git push` is pinned to upstream (`pushInsteadOf`) and never goes through the proxy; Git LFS and anything else the proxy cannot serve is transparently forwarded to github.com. If mirroring fails for any reason, fetches fall back to upstream — the mode never breaks a build.
* SSH remotes (`git@github.com:`) are not rewritten, and container jobs (`container:`) are not accelerated (the proxy listens on the host's loopback). GitHub Enterprise Server is not supported. Linux only.

Use `custom,path=...` records to persist additional directories. Relative paths are resolved against the workspace, so run this action **after** `actions/checkout` when caching workspace-relative directories (e.g. `custom,path=vendor/bundle`). Custom file caches are not supported.

**Disk pressure:** the buildkit cache uses BuildKit's default garbage collection. Other cache modes have no native GC: when the volume drops below 20% free space (or 10% free inodes), the post step emits a warning and a job summary with a per-cache breakdown — increase the `sticky=` label size to fix. If a volume is ever critically full at job start (<5% free space or inodes), all caches on it are automatically reset so the job runs cold instead of failing with "no space left on device", and the next snapshot starts clean.

Other related inputs:

* `sticky_wait_timeout` - how long to wait for the sticky disk to be ready, as a positive Go duration (default `15m`, matching the runner agent's attachment window)
* `sticky_save` - set to `false` to make the job restore-only (default `true`). The job still restores and uses its caches, but its sticky disk is released at job end without a snapshot, so the next job restores the same snapshot. Requires RunsOn v3.5.0 or later; older stacks fail the action instead of saving. All caches in a job share one volume, so one `sticky_save: false` invocation makes the whole job restore-only, even one without `sticky_cache` placed after the invocations that mount caches.

Restore-only jobs let one job keep a cache current while others only read it, for example a matrix that restores what the default branch saved, or pull request jobs that should not save a cache:

```yaml
      - uses: runs-on/action@v2
        with:
          sticky_cache: go
          sticky_save: ${{ github.ref == 'refs/heads/main' }}
```

The action sets a `cache-hit` output: `true` when every requested path was restored from a previous snapshot. It also sets `buildkit-builder` to the stable builder name.

## Development

Commit source changes only. The binaries and `index.js`/`post.js` are build output: `main` and feature branches don't track them, and CI rejects a branch that commits them. Run `make build` to build them locally.

CI builds them once per push (`.github/workflows/ci.yml`), and every test suite runs that build, so tests always exercise the pushed source.

CI also publishes each built branch as `dist/<branch>`. To test a branch or `main` from another repository, use `runs-on/action@dist/<branch>`, for example `runs-on/action@dist/main`. When its branch is deleted, the mirror is deleted only after a reviewer approves the `dist-cleanup` environment, because other repositories may still be testing against it.

## Release

Releases are created by the manual **Release** GitHub Actions workflow. Run it from `main` with a new tag, for example `v2.5.0`, and optionally the `source_sha` of a main commit (default: the tip of main). CI must have passed for that commit's push to main.

The workflow publishes the exact build that CI tested. It commits main's tree at that commit plus the build on top of the release branch (`v2`), with the released main commit as a second parent so release notes list the merged pull requests. It then tags the commit, signs `SHA256SUMS`, pushes the branch and tag together, creates the release with assets and GitHub artifact attestations, and publishes it. The commit's tree is built directly, so a release never has merge conflicts.

Do not create or push release tags locally, and do not push to the release branch by hand.

The repository must have these secrets configured:

* `RELEASE_APP_ID` - GitHub App ID for the release app allowed to bypass release branch rules
* `RELEASE_APP_PRIVATE_KEY` - private key for the release GitHub App
* `RELEASE_GPG_PRIVATE_KEY` - armored private key used to sign `SHA256SUMS`
* `RELEASE_GPG_PASSPHRASE` - passphrase for the private key
* `RELEASE_GPG_KEY_ID` - optional key id when the imported keyring contains more than one signing key

To verify a release:

```bash
gh release download v2.5.0 -R runs-on/action
gpg --verify SHA256SUMS.asc SHA256SUMS
shasum -a 256 -c SHA256SUMS
gh attestation verify main-linux-amd64 -R runs-on/action
```

## Future work

This action will probably host a few other features such as:

- enabling/disabling SSM agent ?
