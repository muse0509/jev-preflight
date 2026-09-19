# Security and data handling

jev-preflight is disabled by default (`defaultEnabled: false`). Enabling it
allows selected, best-effort-redacted source changes to leave your machine for
TypeSafe's Jev API. Do not enable it where sending source to an external service
is prohibited.

Independent open-source project. Not affiliated with or endorsed by TypeSafe AI
or Anthropic.

## Data flow

1. `UserPromptSubmit` creates a private Git baseline of the working state,
   including existing staged, unstaged, and non-ignored untracked changes.
2. `Stop` compares a second private snapshot with that baseline. Selection rules
   exclude paths before the remaining diff and file list are normalized and
   redacted.
3. At most one HTTPS request sends the selected turn diff and eight fixed risk
   questions to TypeSafe. Empty or excluded diffs make no request. Oversized diffs are
   skipped entirely, without truncation or batching.
4. Valid scores can produce a short report or one request for Claude to
   investigate. Final and failed Stops attempt to clean up prompt state and private
   snapshots; a pending continuation or background work retains the baseline.

## What crosses the network boundary

| Data | Handling |
| --- | --- |
| Selected file paths and change status | Included in the redacted diff/file list; changes Git classifies as binary carry path/status metadata only. |
| Selected turn diff | Sent after normalization and best-effort redaction, including surrounding diff context. |
| Eight fixed risk questions | Sent together with `model: jev-latest` and `format: unified_diff`. |
| API key | Sent to TypeSafe in the `Authorization` authentication header, never added to the evaluation JSON payload. |
| User prompt, assistant messages, transcript, cron prompt or schedule bodies | Not used as evaluation inputs; the decoder discards these fields and does not open transcripts. |
| Changes already present at the baseline | Not submitted as a separate diff. Existing source can still appear as context or removed lines in a new change. |
| Excluded files and ignored untracked files | Not submitted. Already-tracked files remain eligible even if matched by an ignore rule. |
| Git-classified binary contents | Not submitted; eligible binary paths/status can still be sent. |
| Local session state, identity hashes, private index and object store | Not uploaded as artifacts. Selected source from snapshots is represented in the diff above. |

These boundaries describe this plugin's TypeSafe request. They do not describe
Claude Code's own data handling. A prompt or credential copied into an eligible
source file becomes source content and is subject to selection and redaction,
not a separate guarantee of exclusion.

Binary classification follows Git's diff output and attributes. Content forced
to text by repository attributes follows the text normalization/redaction path.

## Credentials and destination

The binary reads the key from `CLAUDE_PLUGIN_OPTION_TYPESAFE_API_KEY`, falling
back to `TYPESAFE_API_KEY`. Claude Code manages the sensitive `typesafe_api_key`
plugin option; its credential storage depends on the platform. The binary does
not write the supplied key to files, state, diagnostics, or command arguments.
Never put keys in the repository, `.jev-preflight.json`, command arguments,
issues, or logs.

The production destination is fixed to
`https://api.typesafe.ai/v1/systemone`; repository settings cannot override it.
The client does not follow redirects or retry requests. There is no project
backend between the plugin and TypeSafe. Local proxy and certificate trust
settings remain part of the host environment.

## Redaction and its limits

Pattern-based redaction covers:

- PEM private-key blocks and AWS access-key IDs.
- Authorization, Cookie, and Set-Cookie header values.
- Assignments with names containing password, passwd, secret, token, or API-key
  variants.
- Bearer values, common token formats, and JWT-shaped values.

Redaction runs on selected patch text and file paths before hashing or sending.
It is best effort, not data loss prevention or a secret scanner. Unfamiliar,
encoded, fragmented, or otherwise unmatched secrets can remain. File names,
business logic, personal data, and identifying context can remain even when
recognized secrets are replaced. Exclusions reduce what is sent; they do not
make the remaining source anonymous or safe to disclose.

## Local storage and cleanup

Private Git snapshots temporarily contain **unredacted working-tree content**,
including files later excluded from the API request. Snapshots use a private
index and object store outside the repository; the real Git index, refs, and
object database are not modified. Storage paths overlapping the worktree or Git
metadata are rejected.

Snapshots live under
`<scratchpad_dir>/jev-preflight/<hashed-prompt>/snapshot`, or, without a
scratchpad, `${CLAUDE_PLUGIN_DATA}/tmp/<hashed-prompt>/snapshot`. Session/snapshot
root directories use mode `0700` and state files `0600` on POSIX. Windows permission
bits do not establish equivalent ACL isolation; use a private user profile and
appropriate filesystem permissions. This is not encrypted storage or isolation
from other processes running as the same user.

State JSON contains schema/version, repository/session/prompt identity hashes,
snapshot location, baseline tree ID, continuation count, normalized diff hash,
and notice classes. A separate session notice ledger contains only schema,
identity hashes, and error classes. It survives prompt cleanup to suppress
duplicate notices. Neither JSON state nor diagnostics contain patches, source,
API bodies, prompt text, or assistant output. Diagnostics use fixed error classes.
Hook feedback contains risk identifiers/scores and, in assist mode, selected
redacted paths.

Final or failed Stops attempt to remove private snapshots and prompt state.
Active background tasks, scheduled wakeups, and an issued continuation preserve
them for the next Stop. Crashes or cleanup errors can leave files behind.
Scratchpad cleanup depends on its owner. Fallback storage is checked on later
opens: a bounded scan removes positively identified prompt directories older
than 24 hours, while respecting recent locks. This is opportunistic cleanup,
not a guarantee of deletion within 24 hours. Notice ledgers and abandoned lock
tombstones can remain until the containing storage is removed. File deletion
does not guarantee secure erasure from disks, backups, or filesystem snapshots.

## TypeSafe's published data handling

The following summarizes official public documents reviewed on **2026-09-19**.
It does not establish the settings or terms of a particular account.

| Topic | Published position |
| --- | --- |
| Model training | The Privacy Policy states that Input is not used to train or fine-tune AI/ML models. [Privacy Policy](https://typesafe.ai/legal/privacy-policy) |
| Contract wording | MCA section 4.1 requires prior customer consent before Customer Data enters a training dataset that modifies model weights. Section 4.3 separately permits processing of Telemetry. [Master Customer Agreement](https://typesafe.ai/legal/mca) |
| Standard-account retention | The public pages reviewed do not specify a fixed number of retention days. Personal data may be retained as reasonably necessary for services or business purposes, with longer retention where legally required. [Privacy Policy](https://typesafe.ai/legal/privacy-policy) |
| Processing duration | The DPA describes retention as necessary for the processing purpose and applicable law; it also sets out documented instructions and subprocessor provisions. [Data Processing Addendum](https://typesafe.ai/legal/data-processing) |
| Location | Services are hosted in the US; the policy describes transfer to the US for storage and processing. [Privacy Policy](https://typesafe.ai/legal/privacy-policy) |
| Zero data retention | ZDR is offered to enterprise customers. It is not enabled or guaranteed by this plugin. [TypeSafe legal documentation](https://docs.typesafe.ai/legal) |
| Disclosure and metadata | The policy covers service providers, legal requests and other disclosures, and collection of IP/device/usage information. Review those provisions before submitting sensitive data. [Privacy Policy](https://typesafe.ai/legal/privacy-policy) |

Confirm account-specific retention, ZDR eligibility, subprocessors, and
contractual requirements with TypeSafe before enabling the plugin for sensitive
repositories. A no-training commitment is not a no-retention commitment.

## User responsibility and threat model

You decide whether you have permission to submit repository content and whether
the provider's terms meet your requirements. Review exclusions, protect the
credential environment and local storage, and use independent secret scanning.
Keep authentication credentials out of source even when redaction is enabled.

The plugin does not protect against a compromised host, privileged or same-user
malicious processes, backups retaining deleted files, or provider-side handling
beyond the applicable agreements. It does not guarantee detection of all
secrets or defects, resist every adversarial input, or prove that a change is
safe. Jev scores route investigation; the default threshold is uncalibrated.
Failures allow Claude to finish, so this is not an enforcement or merge gate.
Continue using tests, linters, SAST, secret scanning, and human review.

See [architecture](https://github.com/muse0509/jev-preflight/blob/main/docs/architecture.md)
for snapshot and failure mechanics, and
[verification](https://github.com/muse0509/jev-preflight/blob/main/docs/verification.md)
for tested coverage and pending live checks.
