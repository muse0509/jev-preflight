# Development

The runtime is one Go standard-library-only binary. Building from source needs
Go and Git; the launcher tests also require Bash. On Windows, use Git for Windows
Git Bash, not WSL Bash. Make is a development convenience, not a runtime dependency.
Read [AGENTS.md](../AGENTS.md) before changing the repository.

## Pinned toolchain and quality gate

`.go-version` pins **Go 1.26.7**. Make exports the corresponding `GOTOOLCHAIN`;
`go.mod` separately declares Go 1.23.2 as the minimum language/toolchain version.
Use the pinned version for release bytes, including packaging: a different Go
compiler or Deflate implementation can produce a different archive digest.

```sh
GOTOOLCHAIN=go1.26.7 make check
GOTOOLCHAIN=go1.26.7 make test-race
git diff --check
```

`make check` runs format checking, vet, and tests. `make fmt` applies Go formatting.
Direct commands also work without Make; this shell example works in Git Bash:

```sh
export GOTOOLCHAIN=go1.26.7
go vet ./...
go test ./...
go test -race ./...
git diff --check
```

Race-test availability depends on the host platform/toolchain. Record an actual
environment limitation rather than reporting an unexecuted check as passed.

## Build outputs

```sh
GOTOOLCHAIN=go1.26.7 make build
GOTOOLCHAIN=go1.26.7 make cross-build
GOTOOLCHAIN=go1.26.7 make dev-runtime
```

Every runtime build uses `CGO_ENABLED=0`, `-trimpath`, and `-buildvcs=false`.
These disable cgo and omit local path/VCS build metadata. Keep the same options
when building manually so the release checksum remains reproducible.

| Target | Binary filename |
| --- | --- |
| `darwin-amd64` | `jev-preflight` |
| `darwin-arm64` | `jev-preflight` |
| `linux-amd64` | `jev-preflight` |
| `linux-arm64` | `jev-preflight` |
| `windows-amd64` | `jev-preflight.exe` |
| `windows-arm64` | `jev-preflight.exe` |

`make build` writes the native binary to `dist/<os>-<arch>/`.
`make cross-build` writes all six binaries to those target directories.
`make dev-runtime` writes only the native development binary to
`.tmp/runtime/<os>-<arch>/`.

A direct native build, from Bash or Git Bash:

```sh
export GOTOOLCHAIN=go1.26.7
target="$(go env GOOS)-$(go env GOARCH)"
suffix="$(go env GOEXE)"
mkdir -p "dist/$target"
CGO_ENABLED=0 go build -trimpath -buildvcs=false \
  -o "dist/$target/jev-preflight$suffix" ./cmd/jev-preflight
```

Generated binaries, archives, coverage, and temporary state belong only in
ignored `dist/`, `coverage/`, or `.tmp/`. `make clean` removes only those directories
under the repository root and refuses symlink targets. Keep personal files out
of these output locations. Do not commit generated artifacts.

## Source-checkout launcher

The launcher starts from `CLAUDE_PLUGIN_ROOT`, normalizes Darwin/Linux/Windows
Git Bash and amd64/arm64 names, then checks
`scripts/runtime/<os>-<arch>/jev-preflight[.exe]`. Only if that release binary is
absent does it check `.tmp/runtime/<os>-<arch>/`. A present but unusable release
binary does not silently select the fallback. Missing or unsupported binaries
produce a short stderr diagnostic and allow Claude to finish. Arguments are
passed through unchanged with quoted `exec` arguments.

Build and validate a checkout before a separately authorized live session:

```sh
make dev-runtime
make plugin-validate
```

`plugin-validate` checks `claude --version` first. It skips unavailable CLIs and
versions below 2.1.257 without installing or upgrading Claude Code. On a supported
CLI, it runs `claude plugin validate . --strict`. A skipped validation is not a
successful validation; current results are in [verification](verification.md).

When explicitly opting into a live checkout test, start from a disposable Git
repository with synthetic changes and use
`claude --plugin-dir <relative-path-to-checkout>`. Enable jev-preflight through
`/plugin` and set the sensitive key option or documented environment fallback.
Starting a model session and
sending a live API request are separate from the ordinary test suite; follow the
[release smoke-test procedure](releasing.md#post-release-marketplace-smoke-test).

## Test isolation and CI

Tests create repositories and dynamic fixtures under `t.TempDir()`. They isolate
HOME and Git configuration, disable interactive Git prompts, and set any required
author identity only inside the temporary repository. Never point tests at the
real checkout's index, refs, object store, user HOME, or global Git configuration.

Ordinary TypeSafe HTTP tests use fake servers; network-error tests use controlled test
transports. No ordinary test calls the live TypeSafe endpoint or a Claude model.
Integration tests exercise production Go paths for snapshot invariants, filtering,
redaction, limits, fail-open errors, background/cron waiting, cleanup, and the
single-continuation guard. Launcher tests simulate all six mappings and test
native executables in both release and fallback locations. Package tests verify
layout, executable modes, unsafe paths, SHA pins, and reproducibility.

CI has quality/race checks, Ubuntu/macOS/Windows platform tests, six static
cross-builds, and an Ubuntu package job that checks the committed marketplace
pin and checksum. Cross-compilation does not prove execution on that target.
Read [verification](verification.md) for completed runs and remaining live checks.

## Opt-in TypeSafe validation

`TestLiveTypeSafe` is skipped by default. All three conditions are required:
the `jev_live` build tag, `JEV_PREFLIGHT_LIVE_TEST=1`, and a nonempty inherited
`TYPESAFE_API_KEY`. Without the build tag, ordinary tests remain offline even if
both environment variables are present. CI and package commands do not enable
this tag. The harness does not read repository source or call Claude Code.

First run the offline checks above. Only with explicit authorization and the
credential already supplied securely through the environment, run once:

```sh
JEV_PREFLIGHT_LIVE_TEST=1 GOTOOLCHAIN=go1.26.7 \
  go test -tags=jev_live ./internal/jev -run '^TestLiveTypeSafe$' -count=1 -v
```

The harness makes one credential-check request to `GET /v1/models`, requires
HTTP 200 and an available Jev model, then evaluates a small fixed synthetic diff
through the production Go client and embedded eight-question policy. A failed
credential check stops before evaluation. The successful path uses two API
requests in total. Only a 429 or 529 evaluation response can cause one retry;
`Retry-After` is respected within the overall timeout. The credential check is
never retried, redirects are rejected, and other failures are not retried.

The entire probe has a 30-second deadline. Validation checks all eight Noul
answers, finite probabilities in [0, 1], the
returned model, and usage fields. It does not require a particular probability
or a threshold crossing. Safe logs report request counts and validated result
fields; credentials, headers, raw API errors, and complete request/response
bodies are never printed or written by the harness. Keep any observed timing
or usage in the private validation report; do not publish service benchmarks
without the permission described in [releasing](releasing.md).

This test does not establish real Claude hook behavior, Git snapshot invariants,
or a Jev-triggered continuation. Those are separate from the API contract check;
see [verification](verification.md) and the pre-release
[manual hook procedure](hook-smoke.md). The separate
[marketplace install smoke](releasing.md#post-release-marketplace-smoke-test)
requires publication first.

For a successful manual run, require `PASS` for `TestLiveTypeSafe`, the
`GET /v1/models HTTP 200; jev-latest available` message, a model/usage summary,
and eight `axis=... noul=...` lines. Request counts should be `GET=1 POST=1 total=2`,
or `GET=1 POST=2 total=3` after the single permitted rate-limit/overload retry.
A skipped test can still leave `go test` with exit status zero; it does not
validate the API. Inspect failures by their sanitized class, not by enabling
HTTP tracing or dumping environment variables.

The probe keeps its synthetic fixture and response inspection in memory. It
does not create Git repositories, snapshots, locks, or persistent API artifacts;
Go removes its temporary test-build directory. No repository cleanup command is
needed. After a Terminal session's validation, remove its credential and opt-in
variables from that shell if they are no longer needed:

```sh
unset TYPESAFE_API_KEY JEV_PREFLIGHT_LIVE_TEST
```

The inline opt-in assignment above does not persist in the parent shell.
