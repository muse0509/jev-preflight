# Verification record

This is a record of observed results, not a claim that all release checks have
passed. Source-only documentation updates do not enter the plugin archive;
README changes do and require a new marketplace pin. A prior green run does not
verify a later archive or commit.

## Completed baseline: 2026-09-19 (JST)

Verified implementation HEAD:
`c1d6de37525a8b1363f8e92e682b03bf0ff85282`.

| Evidence | Result |
| --- | --- |
| [PR CI](https://github.com/muse0509/jev-preflight/actions/runs/35363391553) | 11/11 jobs succeeded at the baseline HEAD |
| [Push CI](https://github.com/muse0509/jev-preflight/actions/runs/35363392062) | 11/11 jobs succeeded at the baseline HEAD |
| Platform tests | Windows amd64, macOS arm64, and Ubuntu amd64 passed |
| Quality/race | Formatting, vet, tests, race tests, and diff checks passed |
| Six cross-builds | Darwin, Linux, Windows; amd64 and arm64 for each |
| Ubuntu packaging | Archive, committed pin, and checksum matched; unpacked native launcher ran |

Baseline archive SHA-256 (before the current README reorganization):

```text
33cdc0233ff508b384282d49cc05cae85f168889188fb1896a3d7ce7c1cea102
```

Local verification used macOS arm64, Go 1.26.7, and Apple Git 2.50.1:

- `make check`, `go test -race ./...`, and `git diff --check` passed.
- Static current-platform and all six cross-builds passed.
- The current-platform binary, development launcher, and unpacked launcher
  successfully reported the version.
- Archive layout, executable permissions, marketplace/sidecar checksums, and
  repeated-package digest equality passed.
- Rebuilding all six binaries and the zip in two source directories without
  `.git`, using different locales/timezones and a path containing spaces,
  reproduced the baseline digest. Ubuntu CI independently reproduced it.
- Tests verified the real Git index, refs, object inventory/content/mtime, and
  status invariants; fail-open errors; zero-request no-ops; and one risky
  continuation using isolated temporary repositories and fake APIs.
- Launcher tests exercised all six mappings, unsupported targets, literal
  arguments, and native executables in release and fallback locations. Missing
  Bash fails the tests. Windows lock regression tests held an old directory
  handle open while acquiring its successor; child-reaping tests also passed.

Runtime evidence is limited to the platform/architecture combinations above.
Darwin amd64, Linux arm64, and Windows arm64 remain cross-build-only.
Configured workflows alone are not evidence of successful execution.

## Documentation refresh

The documentation refresh prepared on 2026-09-19 (JST) started from the verified
baseline. It changes README, five source-only documents, and the marketplace
checksum pin. Runtime behavior, policies, hooks, test logic, and workflows are
unchanged at that stage. No commit or push had been made at that point.

Local checks passed with `GOTOOLCHAIN=go1.26.7`: `make check`,
`make prepare-pin VERSION=v0.1.0`, and two consecutive
`make package VERSION=v0.1.0` runs. Both package runs produced the same digest,
matching the marketplace pin and `.zip.sha256` sidecar:

```text
3efa6af21298fc3e4cfc2519ef20403679d7f2de5885d2c26459b0968a3b2b78
```

The twelve-entry archive includes the updated README and all six binaries, with
executable modes for the binaries and launcher. It excludes `docs/` and the
marketplace catalog. README links point to the requested main-branch GitHub
paths, whose source files are present in this diff; they become available there
when these documents reach `main`. Local link/whitespace/path checks and
`git diff --check` passed; generated outputs remain ignored.

The two CI runs above cover the baseline, not this documentation refresh or its
new archive digest. No new cross-host or cross-path reproduction is claimed for
this digest; the two local package runs used the same checkout.

Provider policies and model limits are documented from official sources in
[security](security.md); they do not constitute live account validation. Confirm
the applicable account terms and data permissions before enabling. No measured
latency, cost benchmark, model comparison, or unexecuted validation is claimed.

## Offline live-harness preparation: 2026-09-19 (JST)

Added an explicitly gated `TestLiveTypeSafe` harness without changing the runtime
client, policy, hooks, configuration defaults, or workflows. It uses only a fixed
synthetic diff, the production Go client/request schema, and the embedded eight
Noul questions. Its default is skip. Enabling requires the `jev_live` build tag,
`JEV_PREFLIGHT_LIVE_TEST=1`, and an inherited `TYPESAFE_API_KEY`.

Completed offline checks with Go 1.26.7:

- `JEV_PREFLIGHT_LIVE_TEST=0 make check` passed.
- `JEV_PREFLIGHT_LIVE_TEST=0 go test -race -count=1 ./...` passed.
- Fake-transport regressions covered credential failures before evaluation,
  metadata shape, bounded responses, endpoint/redirect restrictions, sanitized
  errors, cancellation, and at most one 429/529 retry respecting `Retry-After`.
- Tagged fake-only tests passed. A tagged run with opt-in disabled skipped the
  actual probe and reported `GET=0 POST=0 total=0`.
- The uncached race suite rechecked Git index/refs/object/status invariants,
  baseline preservation, secret redaction, one-continuation guards, and
  snapshot/lock cleanup using disposable repositories and fake APIs.
- Two fresh `make package VERSION=v0.1.0` runs reproduced the documentation
  refresh digest above (`3efa6af2...a3b2b78`); ZIP, marketplace pin, and sidecar
  matched. All six runtime paths were present, and `docs/` and the catalog stayed
  excluded. `git diff --check` passed and generated artifacts remained untracked.

**Actual TypeSafe API requests in that Work Mode preparation: 0.** No live
result had been obtained at that stage. The CLI was 2.1.221 and had not been
upgraded; root/archive strict validation and both no-key/live Claude sessions
were unexecuted. The synthetic API harness does not demonstrate Claude hook
behavior.

That preparation preserved the README Status and marketplace pin. See
[development](development.md#opt-in-typesafe-validation) for the exact manual
command, expected output, request budget, and shell cleanup. No commit, push,
merge, tag, Release, or PR comment was created.

## Owner-run live API smoke: 2026-09-19 (JST)

The owner supplied a successful local `TestLiveTypeSafe` result from macOS
Terminal.app. This is owner-reported live evidence, separate from CI and from
the offline Work Mode checks. Only the harness's disposable synthetic diff was
sent; no real user code or secrets were included in the evaluation payload.
The credential was used for authentication, not as evaluation data.

| Check | Reported result |
| --- | --- |
| Credential smoke | `GET /v1/models`: HTTP 200 |
| Model availability | `jev-latest` available |
| Returned model | `jev-1.13.0` |
| Eight-axis evaluation | All eight Noul answers present and valid; PASS |
| Usage | 898 input tokens, 151 output tokens |
| Requests (reported harness counters) | GET 1, POST 1, application retries 0, total 2 |
| Test | `TestLiveTypeSafe`: PASS |

| Axis | Reported Noul value |
| --- | --- |
| auth_boundary | 0.970000 |
| behavior_regression | 0.910000 |
| compatibility | 0.390000 |
| data_integrity | 0.050000 |
| error_handling | 0.710000 |
| input_validation | 0.810000 |
| lifecycle | 0.070000 |
| regression_tests | 0.870000 |

These are single-fixture diagnostics, not threshold calibration or a public
benchmark. The supplied single-run elapsed time is not used as a performance
claim. This test did not execute a Claude Code hook round-trip.

## Manual hook verification preparation: 2026-09-19 (JST)

Before the owner moved all Claude operations to Terminal.app, the explicitly
authorized native-install update (`claude install stable`) completed, changing
Claude Code **2.1.221 to 2.1.267**. It used the existing native installation,
without sudo or a second installation method. No settings, authentication, or
plugins were manually removed.

At that point `claude plugin validate . --strict` with 2.1.267 **failed** on a
missing marketplace description warning. The catalog now supplies the official
top-level `description` field. Validation had not yet been rerun after that fix;
the later owner-run results are recorded below. No real plugin installation,
Claude session, or TypeSafe API request was
performed in Work Mode. No further Claude update or session is authorized here.

The [manual hook procedure](hook-smoke.md) prepares only synthetic code and uses
session-local plugin loading. It distinguishes expected request counts from
observed wire counts and leaves unsuccessful or skipped checks unverified.

The live API test transport now forces fresh HTTP/1.1 connections to prevent
HTTP/2's internal stream retries from escaping its request counter. A local TLS
fake advertises HTTP/2 and verifies HTTP/1.1, separate connections, and no hidden
resend after 429 or connection closure. This changes test support only; the
runtime client, shipped policy, hooks, and defaults are unchanged. The earlier
owner-run result above records the counters reported by that earlier harness,
not an independent packet capture. This amended harness has not been run live.

The updated README requires this new archive SHA-256:

```text
10b3ddd4d8a262cc0e366b21caf8d30046f5e242959204c85b5d8fba1e66dae5
```

Local static builds succeeded for all six targets with Go 1.26.7,
`CGO_ENABLED=0`, `-trimpath`, and `-buildvcs=false`. Pin preparation followed by
two package runs enforcing the pin produced this digest, matching the marketplace and
sidecar. The twelve entries have sorted names, fixed timestamps, deterministic
compression, and executable launcher/runtime modes. Binary formats and
architectures match all six paths; `docs/`, the catalog, and developer smoke
helpers are excluded.

Two independent temporary source directories without `.git` also reproduced
this digest, including a path with spaces, with `UTC`/`C` versus
`Pacific/Honolulu`/`en_US.UTF-8`. Both temporary directories were removed. These
are local reproducibility checks, not a new cross-host or CI result.

Final offline regression results with `GOTOOLCHAIN=go1.26.7`:

- `make check` and `go test -race ./...`: passed.
- `make build cross-build dev-runtime`, `make prepare-pin VERSION=v0.1.0`,
  and two `make package VERSION=v0.1.0` runs: passed.
- The tagged live test with `JEV_PREFLIGHT_LIVE_TEST=0`: skipped, with
  `GET=0 POST=0 total=0`; this is evidence of the gate, not a live pass.
- Fixture preparation against the final unpacked plugin, a local synthetic
  edit, metadata verification, and fixture cleanup: passed without Claude.
- Fake stream tests covered no-key and one-continuation observations, ordering,
  incomplete/error cases, and secret-safe bounded output. They are not evidence
  of a real Claude session.
- `git diff --check` and the source scan found no new credential or local-path
  findings; generated artifacts remain ignored. The existing redaction tests
  contain deliberately synthetic credential-like fixtures.

At that preparation stage, the new fixture script tests skipped Windows. The
finalization below removes that skip and exercises Git Bash paths and symlinks;
existing launcher tests and all six mappings remain unchanged. Local execution
alone adds no Windows runtime claim. **Actual TypeSafe requests in Work Mode: 0.**

## Owner-run strict and hook validation: 2026-09-19 (JST)

The owner supplied these results from Terminal.app using **Claude Code 2.1.267**
and fresh disposable synthetic fixtures. They are user-run manual validation,
not CI results. The TypeSafe contract smoke above remains the reported successful
GET 1 / POST 1 / total 2 run; it was not repeated for this finalization.

| Check | Owner-reported result |
| --- | --- |
| Repository root | `claude plugin validate . --strict`: PASS |
| Freshly unpacked archive | `claude plugin validate <archive> --strict`: PASS |
| Package checksum verification | PASS |
| No-key hook smoke after reauthentication | `no_key_fail_open=PASS` |
| Key-enabled real hook round-trip | `continuation_check=PASS`; exactly one Jev feedback continuation, then completion |
| Fixture Git metadata | File names and bytes unchanged in both successful hook runs |
| Owner cleanup | Fixture, extracted archive, and related shell variables removed |

The first no-key attempt stopped because Claude authentication had expired,
before any tool call or Stop hook. This was a session precondition failure,
not an observed plugin failure. After logging in again, the owner used a fresh
fixture and obtained the successful no-key result below.

| Observed safe summary field | No-key | Key-enabled |
| --- | --- | --- |
| plugin_loaded | true | true |
| read_calls / read_successes | 1 / 1 | 2 / 2 |
| edit_calls / edit_successes | 1 / 1 | 1 / 1 |
| user_prompt_submit_started / completed | 1 / 1 | 1 / 1 |
| stop_started / completed | 1 / 1 | 2 / 2 |
| continuation_outputs | 0 | 1 |
| api_key_notices | 1 | 0 |
| jev_feedback_outputs | 0 | 1 |
| claude_result_success | true | true |

The key-enabled run used the real API and observed Jev feedback, one continuation,
and then termination. It did **not** measure TypeSafe wire request counts. The
product expectation is POST 1 / GET 0 with no application retry; that is a design
expectation, not a request count observed by this hook smoke. Do not add it to
the separately observed API contract smoke count to invent a live total.

These fields remain **UNOBSERVED** in both real hook runs:

```text
jev_requests=UNOBSERVED
baseline=UNOBSERVED
git_cleanup=UNOBSERVED
stop_hook_active=UNOBSERVED
hook_attribution=UNOBSERVED
```

The fixture metadata comparison did not inspect baseline contents, timestamps,
the Stop input flag, exact callback attribution, or runtime snapshot/lock cleanup.
Owner removal of the test directories does not establish runtime cleanup. The
isolated offline tests provide separate evidence for Git/snapshot invariants,
loop guards, bounded client calls, and cleanup; they do not turn these manual
UNOBSERVED fields into PASS.

## Finalization and remaining release checks

The README now summarizes the successful manual checks without timing or
benchmark claims. Its change requires a new package digest and pin; the previous
digests above remain historical evidence. No TypeSafe API call or real Claude
session is repeated in this finalization.

Final local checks on 2026-09-19 (JST), using Go 1.26.7, passed: `make check`,
`go test -race ./...`, `make build`, `make cross-build`, and `git diff --check`.
The new fixture tests now run under Git Bash on Windows without an OS skip,
including path quoting, metadata comparison, and symlink rejection. The fixture
script is pinned to LF line endings. No CI job or assertion was removed or
relaxed. Authentication guidance is documentation-only: the available safe
stream fields do not establish a reliable auth-specific failure category.

Final archive SHA-256:

```text
a3463c74c558a2cf436bce165e476d1a163f10c3cdaab8525d0064da5e9487a8
```

Two package runs matched this SHA, the marketplace pin, and the sidecar. All
twelve expected entries and six binary formats/architectures passed checks;
launcher/runtime executable permissions are intact. Documentation, the catalog,
Git metadata, and developer helpers are excluded. Source/asset scans found no
new credential or local-path findings, and generated output remains ignored.
Two source copies without `.git` reproduced the digest from different paths,
including spaces, with `UTC`/`C` and `Pacific/Honolulu`/`en_US.UTF-8`.

Claude Code **2.1.267** also passed strict validation locally for the final
repository root and a fresh extraction of this final archive. The temporary
extraction and reproduction source copies were removed. These local checks are
separate from the owner's earlier manual sessions and from GitHub Actions.

The first finalization commit, `61fae0d`, had one failure in each of
[PR CI](https://github.com/muse0509/jev-preflight/actions/runs/35433242296) and
[push CI](https://github.com/muse0509/jev-preflight/actions/runs/35433240229): the
new fixture lifecycle test passed a POSIX absolute path to native Windows Git.
The other ten jobs passed, including package/checksum, and the Windows runtime
and launcher tests themselves passed. The fixture helper now changes directory
in Bash before invoking Git with relative arguments; its regression test
disables automatic MSYS path conversion. No assertion, job, or product behavior
was relaxed or removed. This developer-only correction leaves the archive SHA
unchanged. A new run must verify the corrected commit.

CI for this finalization is pending at preparation time. The earlier green runs
cover only their recorded baseline. Check the
[current branch Actions runs](https://github.com/muse0509/jev-preflight/actions?query=branch%3Areview%2Fv0.1.0)
for the exact pushed commit's PR and push results; local/manual checks do not
substitute for those runs.

The remaining release checks are the final revision's green CI and the separate
post-publication marketplace installation smoke; see [releasing](releasing.md).
The manual observation limits above remain documented limitations. Publication,
merge, tagging, and Release creation still require separate authorization.

## Public v0.1.0 release and marketplace smoke: 2026-09-19 (JST)

The remaining v0.1.0 release checks were completed after the finalization record
above. PR #1 was merged into `main` as commit
`8b2e7308b11ea7b58683621cebaa61bff31a0429`, and the annotated tag `v0.1.0`
points to that commit.

| Evidence | Result |
| --- | --- |
| [Tag CI](https://github.com/muse0509/jev-preflight/actions/runs/35434444914) | All 11 jobs passed |
| [Release workflow](https://github.com/muse0509/jev-preflight/actions/runs/35434444900) | All platform, build, and publish jobs passed |
| [GitHub Release](https://github.com/muse0509/jev-preflight/releases/tag/v0.1.0) | Immutable public prerelease with the plugin zip and checksum sidecar |
| Published archive SHA-256 | `a3463c74c558a2cf436bce165e476d1a163f10c3cdaab8525d0064da5e9487a8` |

The owner then exercised the public user path rather than `--plugin-dir` or a
local archive. Claude Code cloned `muse0509/jev-preflight` over HTTPS, validated
and updated the marketplace, installed `jev-preflight@jev-preflight` version
`0.1.0` at user scope, and enabled it. The install reported no archive,
checksum, or manifest error.

Claude Code **2.1.267** loaded the installed marketplace plugin in two fresh,
disposable Git repositories containing only synthetic Go code. No real project
source was used in either session.

| Observed summary field | Public no-key | Public key-enabled |
| --- | ---: | ---: |
| plugin_loaded | true | true |
| read_calls / read_successes | 1 / 1 | 2 / 2 |
| edit_calls / edit_successes | 1 / 1 | 1 / 1 |
| user_prompt_submit_started / completed | 1 / 1 | 1 / 1 |
| stop_started / stop_completed | 1 / 1 | 2 / 2 |
| continuation_outputs | 0 | 1 |
| api_key_notices | 1 | 0 |
| jev_feedback_outputs | 0 | 1 |
| claude_result_success | true | true |
| Harness verdict | `no_key_fail_open=PASS` | `continuation_check=PASS` |

The no-key session used a behavior-preserving edit from
`return owner == actor` to `return actor == owner`. It completed without Jev
feedback or a continuation and emitted the expected missing-key notice. The
key-enabled session used the synthetic authorization mutation
`return owner == actor` to `return true`. It received Jev feedback, performed
exactly one continuation, and then completed without a second edit.

For the key-enabled session, the API key was read silently from `/dev/tty` in a
child Bash process, exported only inside that process, and unset on exit. It was
not placed in a prompt, command argument, repository, or captured log.

The hook event stream did not expose TypeSafe wire request counts. The expected
product behavior for the key-enabled Stop is one POST, no GET, and no
application retry, but that expectation was not measured by this smoke test.
The following fields therefore remain **UNOBSERVED** rather than being promoted
to PASS:

```text
jev_requests=UNOBSERVED
baseline=UNOBSERVED
git_cleanup=UNOBSERVED
stop_hook_active=UNOBSERVED
hook_attribution=UNOBSERVED
```

Offline tests remain the separate evidence for those internal invariants. With
the published artifact installed through the documented marketplace path, both
the no-key fail-open path and the key-enabled single-continuation path have now
passed. This completes the v0.1.0 Public Beta release gates. Later changes must
use a new version and must not rewrite the immutable v0.1.0 tag or release
assets.
