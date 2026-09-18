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

Prompt text, assistant messages, and transcripts are not used. API keys are read
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

The CI toolchain is pinned in `.go-version` (Go 1.26.7); `go.mod` declares the
minimum language/toolchain version. Use the pinned toolchain for verification.
If your local `go` is older, prefix commands with `GOTOOLCHAIN=go1.26.7` or
select that Go version with your usual toolchain manager.

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
all six targets with `CGO_ENABLED=0` and `-trimpath`. `make dev-runtime` writes
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

Development status (2026-09-18): the release path is prepared; **no release was
published during implementation**. These commands require the matching GitHub
Release asset to exist:

```sh
claude plugin marketplace add muse0509/jev-preflight
claude plugin install jev-preflight@jev-preflight
claude plugin enable jev-preflight@jev-preflight
```

Installation uses the versioned GitHub Release zip declared in
`.claude-plugin/marketplace.json`, not a source-only checkout. Supply the
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
private index, objects, and prompt state. Active background tasks keep the
baseline and allow Stop quietly. Different sessions/prompts have separate state.

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

Locally verified on macOS arm64 with Go 1.26.7 and Apple Git 2.50.1:

- `make check` (format, vet, tests), `go test -race ./...`, and `git diff --check`.
- Static current-platform build and all six cross-builds.
- Current-platform executable and development launcher `version` invocation.
- Universal zip assembly, unpacked layout and SHA-256 verification, strict
  validation of the unpacked plugin, and its macOS arm64 launcher invocation.
- Git index, refs, object inventory/content/mtime and status invariants;
  fail-open API failures; zero-request no-ops; exactly one risky continuation.

Tests use only temporary repositories with isolated HOME/Git configuration and fake API
servers; they never call the real TypeSafe endpoint. CI runs quality/race checks,
Ubuntu/macOS/Windows tests, and six-target static builds. A configured CI job is
not evidence of a completed run. Cross-compilation is not runtime verification.
Linux, Windows, and other CPU runtime execution has not been verified locally.

The local Claude Code CLI is 2.1.221: strict validation of the plugin manifest
passes, but repository-root validation selects the new marketplace and rejects
its `archive` source. The [official archive schema](https://code.claude.com/docs/en/plugin-marketplaces#zip-archives)
requires 2.1.224+, while this plugin requires 2.1.257+ for scratchpad hooks.
Marketplace validation with a supported CLI, a real hook round-trip, and an
explicit opt-in API smoke test remain unverified. No Phase 0 timings are claimed
as Go binary or end-to-end performance evidence.

## Prepare a release without publishing

```sh
make package VERSION=v0.1.0
claude plugin validate dist/unpacked/jev-preflight-plugin-v0.1.0 --strict
```

The development-only Go packager creates
`dist/jev-preflight-plugin-v0.1.0.zip`, its matching `.zip.sha256`, and an unpacked
validation directory. It checks the layout, versions, paths, and file modes.
The zip contains the plugin manifest, hooks, policy, launcher, README, LICENSE,
and six binaries under `scripts/runtime/<os>-<arch>/`. The marketplace catalog
stays in the GitHub repository; it is not part of the installed plugin. The
matching checksum asset allows download integrity checking; this is not signing.
All generated files stay ignored. Archive-layout tests extract fixtures under
`t.TempDir()` and check all six runtime paths and required plugin files.

The prepared `release.yml` runs on a pushed `v*` tag. It tests three platforms,
performs static six-target builds, assembles the universal zip and checksum,
and conditionally runs Claude validation if the CLI is available. It does not
install Claude Code in CI. A separate publish job verifies the checksum and
creates a GitHub prerelease titled Public Beta using the already-existing tag.
No release has been created here. Tags should reference reviewed default-branch
commits that already contain the workflow. Before a future version, update the
runtime version, plugin version, marketplace version and versioned archive URL
together; packaging rejects mismatches.

## Deferred

PR/Checks integration, whole-repository reviews, line comments, forced autofix,
other editor integrations, multi-agent orchestration, large-diff batching,
cloud services, databases, telemetry, dashboards, non-Git support, signing,
SBOM, auto-updates, calibration datasets/threshold tuning, demos, and branding.

Licensed under [MIT](LICENSE).
