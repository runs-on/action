# checkout-with-cache

Composite action that restores a cached repository `.git` directory from RunsOn EFS before `actions/checkout`, then updates the cache after checkout. This keeps `actions/checkout` in charge of the checkout while letting large repositories fetch deltas instead of cloning all history every run.

This is useful for large repositories that use `fetch-depth: 0`. It is usually not worth it for small repositories.

```yaml
jobs:
  build:
    runs-on: runs-on=${{ github.run_id }}/runner=2cpu-linux-x64
    steps:
      - uses: runs-on/action/checkout-with-cache@v2
        with:
          encryption_key: ${{ secrets.CHECKOUT_CACHE_KEY }}
```

The action defaults to `/mnt/efs/${GITHUB_REPOSITORY}/.git` and stores files named `repository-<sha>-<timestamp>.tar`. If `encryption_key` is set, cache files are encrypted with `openssl enc -aes-256-cbc -pbkdf2`; otherwise they are plain tar files.

Writes are limited to the repository default branch by default and are skipped for pull request events. The action passes `persist-credentials: false` to `actions/checkout` by default and also removes checkout extraheaders from local Git config before saving `.git`.

## Inputs

| Name | Default | Description |
| --- | --- | --- |
| `encryption_key` | `''` | Optional passphrase used to encrypt/decrypt cache tarballs. |
| `write_branches` | repository default branch | Comma-separated list of branches allowed to write cache files. |
| `keep_files_count` | `5` | Number of cache files to keep. |
| `force_checkout` | `false` | Skip cache restore and force a normal checkout. |
| `force_write` | `false` | Write a cache file even when the current SHA already has one. |
| `cache_dir` | `/mnt/efs/${GITHUB_REPOSITORY}/.git` | Directory where cache files are stored. |
| `fetch_depth` | `0` | Value passed to `actions/checkout`. |
| `persist_credentials` | `false` | Value passed to `actions/checkout`. |
