# jev-preflight

**Catch risky code changes before Claude Code finishes the turn.**

jev-preflight uses TypeSafe's Jev API: it creates a private Git baseline at turn
start, then sends the selected, redacted turn diff at `Stop` to evaluate eight
risk axes in one request. In assist mode, high risk asks Claude to investigate
once more before finishing.

Independent open-source project. Not affiliated with or endorsed by TypeSafe AI or Anthropic.

## Status

**v0.1.0 Public Beta.**
User-run checks verified the live API contract, root/archive strict validation,
no-key hook fail-open, and a key-enabled hook round-trip with exactly one
continuation on Claude Code 2.1.267, using synthetic code only.
CI was pending when this finalization was prepared; consult the
[current branch runs](https://github.com/muse0509/jev-preflight/actions?query=branch%3Areview%2Fv0.1.0)
for the latest result.
See the [verification record](https://github.com/muse0509/jev-preflight/blob/main/docs/verification.md)
for completed checks and remaining release checks.

## Why jev-preflight

Tests and linters can miss risks involving intent, authorization boundaries,
or compatibility. Jev scores direct Claude's attention to changes worth
reinvestigating; they are signals, not proof of defects.

The plugin is not an autofix or merge blocker, and does not replace tests,
linters, SAST, or secret scanning. The default **0.85 threshold is uncalibrated**.

## How it works

1. `UserPromptSubmit` creates a private baseline of the current working state.
2. Claude Code changes the code.
3. `Stop` selects and redacts the diff relative to that baseline.
4. Jev evaluates eight risk axes in one request.
5. Below the threshold, Claude finishes.
6. At or above the threshold in assist mode, Claude gets at most one additional
   investigation, then finishes. It should change code only when it finds evidence.

Active background tasks or scheduled wakeups defer evaluation and keep the baseline.

## What it checks

| Risk axis | Investigation focus |
| --- | --- |
| Behavior regression | Unintended changes to expected behavior |
| Authorization | Access control and trust boundaries |
| Input validation | Invalid or hostile input |
| Data integrity | Consistent, correct stored data |
| Error handling | Failure paths and recovery |
| Compatibility | Existing callers and contracts |
| Lifecycle | Resource, task, and state lifetimes |
| Regression tests | Coverage for behavior changed by this turn |

## Quick start

Requirements: **Claude Code 2.1.257+**, Git, Bash, and your own TypeSafe API key.
On Windows, use Git for Windows with Git Bash on `PATH`; WSL Bash is not this
runtime. Binaries cover macOS, Linux, and Windows on amd64 and arm64.
Go is only needed for [source development](https://github.com/muse0509/jev-preflight/blob/main/docs/development.md).
TypeSafe access may require early access, and API usage may incur TypeSafe charges.

The following commands require the matching GitHub Release archive to be published:

```sh
claude plugin marketplace add muse0509/jev-preflight
claude plugin install jev-preflight@jev-preflight
claude plugin enable jev-preflight@jev-preflight
```

The marketplace installs a versioned release zip and pins its SHA-256.
The plugin ships with `defaultEnabled: false`; review the data boundary below
before explicitly enabling it. In Claude Code's `/plugin` UI, set the sensitive
`typesafe_api_key` option, or supply the key through your environment before
starting Claude Code. Key precedence is:

1. `CLAUDE_PLUGIN_OPTION_TYPESAFE_API_KEY` (the sensitive plugin option)
2. `TYPESAFE_API_KEY` (environment fallback)

Never paste API keys into the repository, configuration files, command arguments,
issues, or logs. Submit a new prompt after enabling to establish a turn baseline.

## Configuration

Place optional, non-secret settings in **`.jev-preflight.json` at the target
repository root**:

```json
{
  "mode": "assist",
  "riskThreshold": 0.85,
  "timeoutMs": 2000,
  "maxDiffBytes": 65536,
  "exclude": ["dist/**", "vendor/**"]
}
```

| Setting | Default | Meaning |
| --- | --- | --- |
| `mode` | `"assist"` | `assist`: one high-risk reinspection; `report`: evaluate and report high risk, then finish; `off`: no snapshot or API evaluation |
| `riskThreshold` | `0.85` | Investigation threshold, inclusive; accepts 0..1 |
| `timeoutMs` | `2000` | API timeout in milliseconds; accepts 1..60,000 |
| `maxDiffBytes` | `65536` | Byte cap on redacted JSON state, including the file list; accepts 1..1,048,576 |
| `exclude` | `[]` | Extra repository-relative path patterns, in addition to built-in exclusions |

The example adds exclusions; other values match the defaults. Patterns use `/`,
exact paths, `*` within a path component, and a final `/**` for descendants.
A rename touching an excluded path is excluded. Unknown fields, trailing JSON,
null values, or invalid settings fail open.

Lockfiles, common build/generated/vendor directories, minified assets, and
documentation/media files are excluded by default. Dependency manifests,
security configuration, migrations, and test code remain eligible.
Changes Git classifies as binary carry only path/status metadata, never binary content.

Each Git command's stdout also has a **4 MiB raw hard limit**, before filtering
and redaction. Exceeding either local limit skips the whole evaluation: no
truncation or batching.

TypeSafe's [model limits](https://docs.typesafe.ai/models) currently specify
**64k tokens per request**, with **32k tokens for `state` plus the longest question**.
`maxDiffBytes` caps the state, excluding the model and questions; it is not a token
guarantee. Larger settings or dense tokenization can cause API rejection; the
plugin then fails open.

## Privacy and data boundary

**Enabling this plugin sends selected, best-effort-redacted code changes directly
from your machine to TypeSafe.**

| Data | Included in the evaluation payload? |
| --- | --- |
| Selected changed-file paths/status and redacted turn diff | Yes |
| Eight fixed risk questions | Yes |
| User prompt, assistant transcript, cron prompt or schedule bodies | No |
| API key | No; sent separately in the authentication header |
| Existing diff from before the baseline | Not submitted as a separate diff; older code can appear as context or removed lines in a new change |
| Excluded paths, ignored untracked files, Git-classified binary content | No; eligible binary paths/status can still be sent |

Git ignore rules do not exclude files already tracked by Git. Prompt and transcript
fields are not read for evaluation; text copied into source is still source content.
Redaction is **best effort, not DLP**. File names, business logic, and unknown
secrets may remain. Do not use the plugin where source code cannot be sent to an
external service.

Private Git snapshots temporarily contain **unredacted content**. Final or failed
Stops attempt cleanup; crashes or cleanup errors can leave them until scratchpad cleanup
or the next applicable fallback TTL cleanup. See
[security and data handling](https://github.com/muse0509/jev-preflight/blob/main/docs/security.md)
for storage, permissions, redaction limits, and the threat model.

TypeSafe's [Privacy Policy](https://typesafe.ai/legal/privacy-policy) states that
Input is not used to train or fine-tune AI/ML models. It does not publish a fixed
retention period for ordinary accounts; personal data may be retained as reasonably
needed to provide services and for other stated purposes. Services are hosted and
processed in the US. [Zero data retention is an Enterprise option](https://docs.typesafe.ai/legal),
not a default guarantee. Review the policy's service-provider and legal-disclosure
terms, the [Data Processing Agreement](https://typesafe.ai/legal/data-processing),
and the [Master Customer Agreement](https://typesafe.ai/legal/mca) for your use.

## Failure behavior

The plugin **fails open**: network errors, timeouts, invalid responses, oversized
diffs, and a missing key do not prevent Claude from finishing. There is at most
**one API request per Stop**, with no retry or redirect, and at most **one
continuation per prompt**. Empty or fully excluded diffs make zero API requests.

## Documentation

These documents live in the source repository and are not included in the plugin zip:

- [Architecture](https://github.com/muse0509/jev-preflight/blob/main/docs/architecture.md): hooks, snapshots, state, and failure protocol.
- [Security](https://github.com/muse0509/jev-preflight/blob/main/docs/security.md): data flow, credentials, provider policies, and limitations.
- [Development](https://github.com/muse0509/jev-preflight/blob/main/docs/development.md): builds, tests, and local plugin setup.
- [Releasing](https://github.com/muse0509/jev-preflight/blob/main/docs/releasing.md): reproducible archives, checksum pins, and release checks.
- [Verification](https://github.com/muse0509/jev-preflight/blob/main/docs/verification.md): completed evidence and pending validation.

Licensed under [MIT](https://github.com/muse0509/jev-preflight/blob/main/LICENSE).
