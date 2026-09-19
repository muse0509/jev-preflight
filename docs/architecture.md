# Architecture

jev-preflight is one Go binary with two Claude Code hook commands. It uses
TypeSafe's Jev API to route one additional investigation, not to generate a
review or prove that a defect exists. The default threshold, 0.85, is uncalibrated.
See [security](security.md) for data boundaries and [verification](verification.md)
for evidence about the current revision.

## Hook lifecycle

1. `UserPromptSubmit` validates the hook identity and resolves the Git repository
   from `cwd`. It creates a private baseline tree for that session and prompt.
2. Claude edits the working tree. Pre-existing staged, unstaged, and untracked
   changes were already included in the baseline; ignored untracked files stay
   ignored. Ignore rules do not remove already-tracked files from consideration.
3. `Stop` compares the baseline with a current tree in the same private object
   database. It handles tracked, untracked, deleted, renamed, and binary changes.
4. The diff is filtered, normalized, and redacted. Empty or fully excluded changes
   make zero API requests. Oversized changes are skipped as a whole.
5. One request evaluates all eight embedded risk questions. Results below the
   threshold allow completion. For high risk, `report` displays a short
   `systemMessage` and finishes; `assist` can request one reinspection. `off` takes no snapshots and
   makes no API request.
6. At most three qualifying axes are selected by descending yes probability,
   with ties sorted by axis ID. Feedback includes the affected file list and
   asks Claude to inspect the actual diff, surrounding code, and tests. It asks
   for code changes only when supported by evidence.

Duplicate prompt submissions retain the original baseline. A Stop without a
usable baseline does not reconstruct one or evaluate changes against `HEAD`.
Unknown upstream JSON fields are accepted. Prompt text, transcript paths,
assistant messages, and cron bodies have no fields in the decoded input model.

An active background task or nonempty `session_crons` list means the session is
waiting: Stop quietly allows completion, preserves the baseline, and makes no
API request. Background task bodies are ignored; cron input uses `[]struct{}`
so only its count is recognized. Once pending work is absent, a later Stop can
evaluate the accumulated turn diff. Waiting does not reset the loop guards.

## Private Git snapshots

`internal/gitstate` resolves canonical worktree, worktree-specific Git directory,
common Git directory, index, and object-store paths. Snapshot locations that
overlap the worktree or real Git metadata, including symlink aliases, are rejected.
Git commands use `exec.CommandContext` with explicit arguments, not shell strings.

The first snapshot copies the real index byte-for-byte to reuse its stat cache.
If there is no index, it starts from the `HEAD` tree, or an empty index for an
unborn repository. Split indexes are expanded in a temporary private Git
directory with copies of the shared-index files; this avoids refreshing the
real shared index's timestamp.

Snapshot commands disable fsmonitor, split indexes, the untracked cache, hooks,
automatic maintenance, paging, interactive credentials, optional locks, lazy
fetches, and replacement objects. Configured clean/process filters are overridden
for these commands with `process=`, `clean=cat`, and `required=false`, so filters
such as Git LFS are not executed. Inherited Git routing variables are removed.

`git add -A` and `git write-tree --missing-ok` write only to the private index and
object store. Read commands can see real objects through a read-only alternate.
Write commands omit that alternate: Git can refresh an existing alternate
object's timestamp even when its content is unchanged. `--missing-ok` permits
unchanged copied-index entries to reference those real objects without writing
to them.

Tree diffs disable external diff drivers and text conversion, enable rename
detection, and fix diff formatting and ordering. File lists are parsed as
NUL-delimited data where supported, preserving spaces, newlines, leading dashes,
quotes, and shell metacharacters in filenames. Tests compare the real index,
refs, status, and object inventory/content/timestamps before and after snapshot
operations, including unborn repositories and linked worktrees.

## Selection and resource limits

The path policy excludes lockfiles, common generated/build/vendor directories,
minified assets, and documentation/media files. Dependency manifests, security
configuration, migrations, and test code remain eligible. User exclusions accept
repository-relative exact paths, component-local `*`, and a final `/**`. A rename
touching an excluded path is excluded. Changes Git classifies as binary carry
path/status metadata without a binary body.

Selected patches and filenames are ordered, normalized to UTF-8, and redacted.
The resulting canonical JSON state is hashed with SHA-256 for the loop guard.
There are two independent byte limits:

| Limit | Purpose |
| --- | --- |
| `gitstate.RawOutputLimit`: 4 MiB per Git command's stdout | Bounds raw patch and filename-list capture before parsing/redaction. Overflow discards the result and terminates and reaps the Git child. |
| `maxDiffBytes`: 65,536 by default; accepted range 1–1,048,576 | Bounds the selected, redacted canonical JSON state, excluding the model and questions. |

An oversized diff follows the `diff_too_large` fail-open path, with zero requests
and no truncated payload or batching. Context cancellation also terminates and
reaps the Git child. These byte limits do not guarantee compliance with the
provider's token limits; see [configuration](../README.md#configuration).

## Storage, locking, and cleanup

Snapshot paths are derived from hashes of the canonical repository, session, and
prompt identities:

```text
<scratchpad_dir>/jev-preflight/<hashed-prompt>/snapshot/
${CLAUDE_PLUGIN_DATA}/tmp/<hashed-prompt>/snapshot/   # fallback
```

Session/snapshot root directories use mode 0700 and state files 0600 on POSIX.
Windows mode bits do not provide equivalent ACL isolation. Private Git objects can contain unredacted
working-tree content; they are distinct from the body-free state JSON.

Versioned state records identity hashes, snapshot location, baseline tree ID,
continuation count, last normalized diff hash, and notice classes. A separate
session ledger stores only identity hashes and emitted error classes. State
updates use a temporary file and atomic rename in the same directory.

A non-waiting per-prompt directory lock prevents concurrent evaluations; a
separate session notice lock prevents duplicate notices. Lock release verifies
the owner's file identity, renames the directory to a fresh sibling, then removes
it nonrecursively. This avoids reusing a Windows name whose deletion is pending
on another open handle. Repeated unlock calls cannot release a successor's lock.

Three guards prevent repeated investigations:

1. `stop_hook_active` prevents re-evaluation and continuation.
2. A persisted continuation count permits at most one continuation per prompt,
   even if the reinspection changes the diff.
3. The same normalized diff hash is not evaluated again within that prompt.

The hash is saved before the API call. The continuation count and hash are saved
before returning feedback. A normal final or failed Stop attempts to remove the
owned prompt directory, including its index, objects, and state. Pending work and the
first continuation retain it for the next Stop.

Crashes can leave private content behind. Scratchpad lifetime is controlled by
the host. Fallback cleanup runs on later fallback-storage opens; owned prompt
directories become eligible after 24 hours. The sweep is bounded and checks
ownership and containment rather than deleting arbitrary paths. It preserves
recent locks; an expired abandoned lock can remain as an empty tombstone after
private data is removed, preventing an old prompt from replaying. This is not a
scheduled deletion guarantee. Notice ledgers can outlive prompt snapshots.

## Fail-open protocol

The endpoint is fixed to `https://api.typesafe.ai/v1/systemone`. The client uses
`jev-latest`, validates every expected `noul` answer as a finite number in [0, 1],
and accepts unrelated additive response fields. It makes at most one request per
Stop, with no application retry or redirect. Responses are capped at 64 KiB.
The configured API timeout sits within an 80-second runner deadline; hook wiring
allows 90 seconds. These are limits, not measured performance claims.

Missing keys, invalid config, API/network failures, timeouts, invalid responses,
and oversized diffs allow Claude to finish. Hook commands return exit status 0;
stdout contains only hook JSON or is empty for a no-op. Diagnostics use fixed
classes on stderr. A non-blocking notice is emitted at most once per session and
error class when safe storage is available. Invalid input or inaccessible storage
may produce only stderr. Misuse of the CLI outside hooks can return nonzero.

Reinspection uses `hookSpecificOutput.additionalContext` with
`hookEventName: "Stop"`, not `decision: "block"`. A report uses top-level
`systemMessage`. Probabilities remain investigation priorities, not evidence.

## Package map and scope

| Location | Responsibility |
| --- | --- |
| `cmd/jev-preflight` | CLI and hook entrypoint |
| `internal/hook` | Input/output protocol and turn workflow |
| `internal/gitstate` | Private snapshots, bounded Git output, tree diffs |
| `internal/diff`, `internal/redact` | Selection, normalization, and best-effort redaction |
| `internal/session` | State, locks, notices, and cleanup |
| `internal/config` | Strict non-secret repository settings |
| `internal/policy`, `policies/default.json` | Embedded, versioned eight-axis policy and selection |
| `internal/jev` | TypeSafe request and response contract |
| `hooks/hooks.json`, `scripts/run` | Exec-form hook wiring and thin platform launcher |
| `scripts/package` | Development-only archive preparation and verification |

Deferred work includes PR/Checks integration, whole-repository reviews, line
comments, forced autofix, other editor integrations, multi-agent orchestration,
large-diff batching, cloud services, databases, telemetry, dashboards, non-Git
support, signing, SBOM, auto-updates, calibration datasets/threshold tuning,
demos, and branding.

Contract references: [Claude hooks](https://code.claude.com/docs/en/hooks),
[Claude plugins](https://code.claude.com/docs/en/plugins-reference),
[TypeSafe API](https://docs.typesafe.ai/api), and
[Git environment variables](https://git-scm.com/docs/git#Documentation/git.txt-codeGITOBJECTDIRECTORYcode).
