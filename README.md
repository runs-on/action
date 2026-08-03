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

Possible values:

* `inline` - Display costs in the action log output (default)
* `summary` - Display costs in the action log output and in the GitHub job summary
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
| `disk` | `used_percent`, `inodes_used` |
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
* `disk` - Disk metrics (`used_percent`, `inodes_used`)
* `io` - I/O metrics (`io_time`, `reads`, `writes`)
* Comma-separated combinations (e.g., `cpu,network,memory,disk,io`)
* Empty string - No additional metrics (default)

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

Only available for Linux runners.

Configures [`sccache`](https://github.com/mozilla/sccache) so that you can cache the compilation of C/C++ code, Rust, as well as NVIDIA's CUDA.

The only parameter it can take for now is `s3`, which will auto-configure the [S3 cache backend for sccache](https://github.com/mozilla/sccache/blob/main/docs/S3.md), using the RunsOn S3 cache bucket that comes for free (with crazy speed and unlimited storage) with your RunsOn installation.

Example:

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64/extras=s3-cache
    steps:
      - uses: runs-on/action@v2
        with:
          sccache: s3
      - uses: mozilla-actions/sccache-action@v0.0.9
      - run: # your slow rust compilation
```

Possible values:

* `s3` - Use RunsOn S3 cache bucket for sccache backend
* Empty string - Disable sccache configuration (default)

What this does under the hood is the equivalent of:

```bash
echo "SCCACHE_GHA_ENABLED=false" >> $GITHUB_ENV
echo "SCCACHE_BUCKET=${{ env.RUNS_ON_S3_BUCKET_CACHE}}" >> $GITHUB_ENV
echo "SCCACHE_REGION=${{ env.RUNS_ON_AWS_REGION}}" >> $GITHUB_ENV
echo "SCCACHE_S3_KEY_PREFIX=cache/sccache" >> $GITHUB_ENV
echo "RUSTC_WRAPPER=sccache" >> $GITHUB_ENV
```

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

On RunsOn runners, the action fails if the sticky disk is absent or does not
become ready before `sticky_wait_timeout`. On any other runner (for example a
workflow falling back to GitHub-hosted runners), the action skips all
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
| `git` | `checkout` | Git repository mirrors (local git proxy serving github.com fetches from the sticky disk) |
| `gradle` | | `~/.gradle/caches`, `~/.gradle/wrapper` |
| `maven` | | `~/.m2/repository` |
| `playwright` | | `~/.cache/ms-playwright` |
| `custom` | | One or more paths supplied with `path=` |

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

The `git` mode accelerates `actions/checkout` — and any other `git fetch`/`git clone` of a github.com repository during the job — without changing your checkout step. It starts a local git proxy whose bare repository mirrors live on the sticky disk, and rewrites `https://github.com/` fetch URLs to it (global `url.insteadOf`). The mirror syncs once per repo per job (only new objects cross the network); the fetch itself, including shallow `fetch-depth: 1` packs, is served at disk speed by `git upload-pack` on the runner.

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

* Mirror syncs authenticate with the `token` input (default: `${{ github.token }}`). Checking out **other private repositories** requires passing a PAT with access to them, since the rewritten URLs no longer match the credentials configured by `actions/checkout`.
* `git push` is pinned to upstream (`pushInsteadOf`) and never goes through the proxy; Git LFS and anything else the proxy cannot serve is transparently forwarded to github.com. If mirroring fails for any reason, fetches fall back to upstream — the mode never breaks a build.
* SSH remotes (`git@github.com:`) are not rewritten, and container jobs (`container:`) are not accelerated (the proxy listens on the host's loopback). GitHub Enterprise Server is not supported. Linux only.

Use `custom,path=...` records to persist additional directories. Relative paths are resolved against the workspace, so run this action **after** `actions/checkout` when caching workspace-relative directories (e.g. `custom,path=vendor/bundle`). Custom file caches are not supported.

**Disk pressure:** the buildkit cache uses BuildKit's default garbage collection. Other cache modes have no native GC: when the volume drops below 20% free space (or 10% free inodes), the post step emits a warning and a job summary with a per-cache breakdown — increase the `sticky=` label size to fix. If a volume is ever critically full at job start (<5% free space or inodes), all caches on it are automatically reset so the job runs cold instead of failing with "no space left on device", and the next snapshot starts clean.

Other related inputs:

* `sticky_wait_timeout` - how long to wait for the sticky disk to be ready, as a positive Go duration (default `15m`, matching the runner agent's attachment window)

The action sets a `cache-hit` output: `true` when every requested path was restored from a previous snapshot. It also sets `buildkit-builder` to the stable builder name.

## Development

Make your source code changes in a commit, then rebuild and commit the generated binaries and JS files:

```
make dist
```

## Release

Releases are created by the manual **Release** GitHub Actions workflow. Run it from the `v2` branch with a new tag, for example `v2.2.0`. The workflow builds the distributed artifacts in CI, commits them to the release branch, tags that artifact commit, creates a draft release with assets, signs `SHA256SUMS`, creates GitHub artifact attestations, and publishes the draft.

Do not create or push release tags locally. The tag must be created by the workflow after the CI-built artifacts have been committed.

The repository must have these secrets configured:

* `RELEASE_APP_ID` - GitHub App ID for the release app allowed to bypass release branch rules
* `RELEASE_APP_PRIVATE_KEY` - private key for the release GitHub App
* `RELEASE_GPG_PRIVATE_KEY` - armored private key used to sign `SHA256SUMS`
* `RELEASE_GPG_PASSPHRASE` - passphrase for the private key
* `RELEASE_GPG_KEY_ID` - optional key id when the imported keyring contains more than one signing key

To verify a release:

```bash
gh release download v2.2.0 -R runs-on/action
gpg --verify SHA256SUMS.asc SHA256SUMS
shasum -a 256 -c SHA256SUMS
gh attestation verify main-linux-amd64 -R runs-on/action
```

## Future work

This action will probably host a few other features such as:

- enabling/disabling SSM agent ?
