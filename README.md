# jev-preflight

A turn-scoped semantic risk gate for Claude Code, powered by Jev.

**v0.1.0 — Public Beta.** Jev routes investigation effort; its scores are not
proof of defects. The default threshold of 0.85 has not been calibrated.

## What it does

At `UserPromptSubmit`, the plugin snapshots the working state into a private Git
tree. At `Stop`, it compares that baseline with a second private tree and sends
only the new, selected changes to Jev. Existing staged, unstaged, and untracked
changes are part of the baseline. Ignored files stay ignored.

Eight risk questions run in one request: behavior regression, authorization,
input validation, data integrity, error handling, compatibility, lifecycle, and
regression tests. Up to three scores at or above the threshold ask Claude to
inspect the diff, surrounding code, and tests once. Claude should change code
only when it finds evidence. The next Stop always allows completion.

This is not a reviewer that writes long explanations, a forced autofix, a
whole-repository audit, or a replacement for tests, linters, SAST, or secret
scanning. It does not create PRs or contact GitHub.

## Privacy and security

**Selected and best-effort-redacted diffs are sent directly from your machine
to the TypeSafe API when you enable this plugin.** Requests can incur service
costs. The plugin ships disabled (`defaultEnabled: false`).

Prompt text, assistant messages, cron prompts/schedules, and transcripts are not
used. Only the number of scheduled wakeups is retained. API keys are read
only from the environment, never passed in arguments or written by this binary
to files, logs, or state. The sensitive plugin option is managed by Claude Code;
its credential storage depends on the platform.

PEM private keys, AWS access keys, common tokens, credential assignments,
Authorization, Cookie, and Set-Cookie values are redacted before sending. This
is best effort, not DLP: unfamiliar secrets, sensitive business logic, file
names, or identifying context can remain. Review your exclusions before enabling.
Do not use this plugin where sending source to an external service is prohibited.

Private Git objects temporarily contain unredacted working-tree content. Their
directory is private (0700; state files 0600 on POSIX), outside the repository,
and removed after a final or failed Stop. Windows permission bits do not provide
POSIX ACL isolation; use a private user profile. A crash can leave snapshots until
scratchpad cleanup or fallback TTL cleanup. Same-user malicious processes are
outside this isolation boundary.

State JSON contains only schema/version, identity hashes, snapshot location,
baseline tree ID, continuation count, normalized diff hash, and notice classes.
A separate session notice ledger stores only hashes and error classes. No patch,
API body, or source is stored in these JSON files or diagnostics.

TypeSafe retention, service payload limits, and terms have **not** been verified
for publication. Confirm them with the provider before a public release; see the
[official TypeSafe documentation](https://docs.typesafe.ai/api).

## Requirements

- Claude Code **2.1.257 or newer**.
- Git and Bash. Windows uses Git for Windows with Git Bash.
- A TypeSafe API key supplied through the sensitive plugin option or environment.
- A binary for Darwin, Linux, or Windows on amd64 or arm64.

Go is needed only to build from source. No Node, npm, Python, jq, curl, database,
or background service is needed at runtime. Make is a developer convenience;
Windows users can use the direct Go commands below.

## Build and test from source

The toolchain is pinned in `.go-version` (Go 1.26.7), which Make also selects via
`GOTOOLCHAIN`; `go.mod` declares the minimum language/toolchain version.
For direct Go commands, prefix them with `GOTOOLCHAIN=go1.26.7` or select that
version with your usual toolchain manager.

```sh
make check
make test-race
make build
make cross-build
make dev-runtime
make plugin-validate
```

Direct quality commands, including on Windows:

```sh
go vet ./...
go test ./...
go test -race ./...
git diff --check
```

`make build` writes to `dist/<os>-<arch>/`. `make cross-build` statically builds
all six targets with `CGO_ENABLED=0`, `-trimpath`, and `-buildvcs=false`. The same
flags apply to every runtime build so Git metadata cannot change release bytes.
`make dev-runtime` writes
the current platform binary only to `.tmp/runtime/<os>-<arch>/`. The launcher
first checks `scripts/runtime/<os>-<arch>/`, then the development fallback.
Missing or unsupported binaries produce a short stderr diagnostic and allow
Claude to finish. `make clean` removes only the root `dist/`, `coverage/`, and
`.tmp/` directories; keep personal files out of those generated-output paths.

## Enable and configure a source checkout

1. Build the local binary with `make dev-runtime` and validate with
   `claude plugin validate . --strict`.
2. Start Claude Code with `claude --plugin-dir /absolute/path/to/jev-preflight`.
3. Use `/plugin` to enable **jev-preflight** explicitly and set its sensitive
   `typesafe_api_key` option. Alternatively supply `TYPESAFE_API_KEY` through
   your environment before starting Claude Code. Never put a key in the repo.
4. Submit a new user prompt to establish the baseline, then make a code change.

## Install the Public Beta release

These commands require the matching versioned GitHub Release asset to exist:

```sh
claude plugin marketplace add muse0509/jev-preflight
claude plugin install jev-preflight@jev-preflight
claude plugin enable jev-preflight@jev-preflight
```

Installation uses the versioned GitHub Release zip declared in
`.claude-plugin/marketplace.json`, not a source-only checkout. Its `source.sha256`
pins the zip bytes, which Claude Code verifies automatically during installation.
Supply the
sensitive `typesafe_api_key` option when enabling, or use the environment fallback.
On Windows, put Git for Windows Bash on PATH; WSL Bash is not this runtime.

## Repository configuration

API key precedence is `CLAUDE_PLUGIN_OPTION_TYPESAFE_API_KEY`, then
`TYPESAFE_API_KEY`. The endpoint is fixed to
`https://api.typesafe.ai/v1/systemone`; repository config cannot change it.

Place optional non-secret settings in the target repository root:

```json
{
  "mode": "assist",
  "riskThreshold": 0.85,
  "timeoutMs": 2000,
  "maxDiffBytes": 65536,
  "exclude": ["dist/**", "vendor/**"]
}
```

Defaults match the example except `exclude`, which defaults to `[]` in addition
to the built-in exclusions. `riskThreshold` accepts 0..1, `timeoutMs` 1..60000,
and `maxDiffBytes` 1..1048576. Unknown fields, trailing JSON, null values, and
invalid settings fail open with a notice. Exclusions use slash-separated paths
relative to the repository: exact paths, `*` within a path component, and a
final `/**` for descendants. A rename touching an excluded path is excluded.

- **assist**: high risk returns Stop `additionalContext` for one reinspection.
- **report**: high risk produces a short user-facing `systemMessage` and finishes.
- **off**: no snapshot or API evaluation.

Lockfiles, common generated/build/vendor directories, minified assets, and
documentation/media files are excluded by default. Dependency manifests,
security config, migrations, and test code remain eligible. Binary changes send
only path/status metadata. The selected diff and file list are normalized,
redacted, then hashed as deterministic UTF-8 JSON. If that representation exceeds
`maxDiffBytes`, the entire evaluation is skipped; no truncation or batching.

Before parsing or redaction, each Git command's stdout has a separate **4 MiB
raw hard limit** (`gitstate.RawOutputLimit`). This also bounds patch and filename
list capture. Crossing that limit discards the entire result, terminates and
reaps the Git child, makes zero API requests, and reports `skipped: diff too
large` for an oversized diff. Cancellation also terminates and reaps the child.
The raw limit protects local memory; `maxDiffBytes` still limits the redacted
canonical JSON sent to TypeSafe. A raw diff is not truncated to fit either limit.

## Architecture and failure behavior

`cmd/jev-preflight` wires `hook`, `config`, `gitstate`, `diff`, `redact`, `session`,
`policy`, and `jev`. The versioned eight-axis policy in `policies/default.json`
is embedded into every binary. `hooks/hooks.json` uses exec-form arguments to
invoke the thin Bash launcher without shell interpolation of input.

Snapshots copy the real index, expand split indexes privately, disable filters,
fsmonitor, hooks, maintenance, prompts, and optional locks, then run private
`git add -A` and `write-tree`. Reads use the real object store as an alternate.
Writes omit the alternate because Git can refresh existing alternate-object
mtimes; `write-tree --missing-ok` preserves unchanged copied-index references.
Neither the real index, refs, nor object database is modified. Snapshot paths
that overlap the worktree or Git metadata are rejected.

Snapshots live under `<scratchpad_dir>/jev-preflight/<hashed-prompt>/snapshot`.
Without a scratchpad they use `${CLAUDE_PLUGIN_DATA}/tmp/<hashed-prompt>/snapshot`.
Fallback cleanup removes positively identified expired prompt directories after
24 hours; it never sweeps arbitrary paths. An abandoned lock can leave an empty
hashed tombstone to prevent replay. Normal final Stops remove their
private index, objects, and prompt state. Active background tasks or a nonempty
`session_crons` list keep the baseline and allow Stop quietly without an API
request. A later Stop with no pending work evaluates the accumulated turn diff.
Cron bodies are discarded during decoding. Waiting never resets the Stop or
one-continuation guards. Different sessions/prompts have separate state.

Three guards prevent repeated investigations: `stop_hook_active`, a persisted
one-continuation limit, and the last normalized diff hash. A per-prompt lock
prevents concurrent evaluations. Count/hash updates use atomic rename before
returning feedback. Missing baselines, empty/excluded changes, and exhausted
guards make zero API requests.

HTTP errors, timeouts, network failures, malformed/partial answers, invalid
config, and oversized diffs allow completion. There is at most one request per
Stop, with no retry or redirect. Responses are capped at 64 KiB. Hook commands
exit 0; stdout is hook JSON only. Diagnostics use fixed classes on stderr, and
a non-blocking notice is shown at most once per session/error class when safe
session storage is available. Invalid input or inaccessible storage may only
produce stderr. CLI usage errors outside hooks exit nonzero.

Contracts were checked against the official
[hooks reference](https://code.claude.com/docs/en/hooks),
[plugin reference](https://code.claude.com/docs/en/plugins-reference),
[TypeSafe API](https://docs.typesafe.ai/api), and
[Git environment documentation](https://git-scm.com/docs/git#Documentation/git.txt-codeGITOBJECTDIRECTORYcode).

## Current verification

Local verification record (2026-09-19), macOS arm64, Go 1.26.7, Apple Git 2.50.1:

- `make check` (format, vet, tests), `go test -race ./...`, and `git diff --check`.
- Static current-platform build and all six cross-builds.
- Current-platform executable and development launcher `version` invocation.
- Universal zip assembly, unpacked layout, marketplace/sidecar SHA-256 checks,
  repeated-package digest equality, and its macOS arm64 launcher invocation.
- Git index, refs, object inventory/content/mtime and status invariants;
  fail-open API failures; zero-request no-ops; exactly one risky continuation.

Tests use only temporary repositories with isolated HOME/Git configuration and fake API
servers; they never call the real TypeSafe endpoint. CI runs quality/race checks,
Ubuntu/macOS/Windows tests, six-target static builds, and strict packaging on
Ubuntu to compare its archive with the marketplace pin. A configured CI job is
not evidence of a completed run. Cross-compilation is not runtime verification.
Linux, Windows, and other CPU execution of this revision have not been verified
locally. The [previous CI run](https://github.com/muse0509/jev-preflight/actions/runs/35356499388)
at `bfd1ddbc` passed Ubuntu/macOS tests, quality checks, and all six cross-builds,
but failed the Windows launcher simulation because PATH-based fake `uname`
selection used the host executable. The revised tests define a Bash function, cover all
six mappings and unsupported platforms, and launch a native test executable via
both runtime locations. Windows results require the next GitHub Actions run;
local simulation is not Windows runtime evidence.

The local Claude Code CLI is 2.1.221, below the required 2.1.257. Marketplace and
unpacked-plugin validation were **not run for this revision**. `make plugin-validate`
skips unavailable or older CLIs; it does not install or upgrade them. Supported
CLI validation, a real hook round-trip, and an opt-in live API smoke test remain
release checks. No live Claude model or TypeSafe API was called. No Phase 0
timings are claimed as Go binary or end-to-end performance evidence.

## Prepare a release without publishing

```sh
make prepare-pin VERSION=v0.1.0
# Review the printed digest and explicitly update marketplace source.sha256.
make package VERSION=v0.1.0
make plugin-validate
make plugin-validate PLUGIN_DIR=dist/unpacked/jev-preflight-plugin-v0.1.0
```

Finish code, policy, launcher, and README edits before preparing the digest.
`prepare-pin` bypasses only checking the existing digest and never edits source.
Copy the actual printed 64-character lowercase SHA-256 into the catalog, then
run normal `package` twice. Normal packaging rejects missing, invalid, or
mismatched pins; it never rewrites the catalog. Since the catalog is excluded
from the archive, updating only its digest does not change the zip bytes.
Use the pinned Go toolchain for all builds and keep `-buildvcs=false` enabled.
The packager sorts entries, fixes UTC timestamps and permissions, and uses Go's
Deflate implementation. `.gitattributes` fixes packaged text to LF on every OS.
Regression tests compare archives across source paths, timestamps, permissions,
locales, and timezones. A release pin must also pass the Ubuntu CI package job;
matching builds on one local host alone are not cross-host evidence.

The development-only Go packager creates
`dist/jev-preflight-plugin-v0.1.0.zip`, its matching `.zip.sha256`, and an unpacked
validation directory. It checks the layout, versions, paths, and file modes.
The zip contains the plugin manifest, hooks, policy, launcher, README, LICENSE,
and six binaries under `scripts/runtime/<os>-<arch>/`. The marketplace catalog
stays in the GitHub repository; it is not part of the installed plugin. The
catalog pin enables automatic installer verification; the matching checksum
asset supports manual verification. Neither mechanism is signing.
All generated files stay ignored. Archive-layout tests extract fixtures under
`t.TempDir()` and check all six runtime paths and required plugin files.

The prepared `release.yml` runs on a pushed `v*` tag. It tests three platforms,
performs static six-target builds, assembles the universal zip and checksum,
and rejects any difference between the committed marketplace pin and the new
archive before upload or publication. It runs Claude validation only when the
CLI is at least 2.1.257; older/missing CLIs are reported as unverified. It does not
install Claude Code in CI. A separate publish job verifies the sidecar checksum and
creates a GitHub prerelease titled Public Beta using the already-existing tag.
Tags should reference reviewed default-branch
commits that already contain the workflow. Before a future version, update the
runtime version, plugin version, marketplace version and versioned archive URL
together, then prepare and pin the final archive digest; packaging rejects
mismatches.

Before publishing, require a green OS matrix for the exact revision, validate
both the catalog and unpacked plugin with Claude Code >= 2.1.257, and confirm
TypeSafe retention/terms. Live checks need separate opt-in: use a disposable Git
repository with synthetic code, enable the plugin, submit one edit prompt, and
observe Stop and its possible single continuation. This invokes the Claude
model; Jev receives selected/redacted file paths and the turn diff plus the eight
fixed risk questions. Prompt, assistant, and cron bodies are not sent to Jev.

## Deferred

PR/Checks integration, whole-repository reviews, line comments, forced autofix,
other editor integrations, multi-agent orchestration, large-diff batching,
cloud services, databases, telemetry, dashboards, non-Git support, signing,
SBOM, auto-updates, calibration datasets/threshold tuning, demos, and branding.

Licensed under [MIT](LICENSE).
