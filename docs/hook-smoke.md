# Manual Claude hook smoke test

This is a procedure to run later, not a record of a completed test. It starts
real Claude sessions and, in the second phase only, can call TypeSafe. Do not
run it as part of ordinary tests, CI, or packaging. Use a macOS or Linux shell
with Bash, Git, `unzip`, and `shasum`. The fixture script also has offline tests
for Git Bash on Windows; these session commands target macOS/Linux.

The fixture contains only a small synthetic Go function. Preparation copies an
already unpacked release plugin without modifying its manifest, hooks, launcher,
runtime, policy, or default configuration. It creates an isolated Git repository
under ignored `.tmp/`; the source repository is not the Claude working directory.
No installation or persistent plugin enablement is needed.

## 1. Prepare and validate locally

Run from the project checkout, with Claude Code already installed and authenticated.
The plugin requires 2.1.257 or later; the additional isolation flags in this recipe
were checked with 2.1.267. First inspect the version and help, and stop if any flag
below is unavailable. These steps do not update or install Claude:

```sh
project="$PWD"
claude --version
claude --help
claude plugin validate --help
claude auth status --text
```

Confirm that Claude authentication is available before either session. If the
status reports missing or expired authentication, stop and resolve that outside
this procedure; do not run a login, update, or installation as part of the smoke
test. This preflight does not prove that a server will accept the credential.
A later Claude authentication error is an unmet precondition, not evidence of a
plugin failure. Record the hook test as blocked by Claude authentication and
leave unexercised checks unverified. The safe stream summary does not classify
authentication failures separately; do not infer their cause from `FAIL` alone.

After confirming flag support, prepare the archive and fixture. This chain stops
on the first failing command:

```sh
make package VERSION=v0.1.0 &&
(cd dist && shasum -a 256 -c jev-preflight-plugin-v0.1.0.zip.sha256) &&
test ! -L .tmp &&
mkdir -p .tmp &&
archive_check=$(mktemp -d "$project/.tmp/hook-archive.XXXXXXXX") &&
unzip -q dist/jev-preflight-plugin-v0.1.0.zip -d "$archive_check" &&
plugin="$archive_check" &&
claude plugin validate . --strict &&
claude plugin validate "$plugin" --strict &&
GOTOOLCHAIN=go1.26.7 CGO_ENABLED=0 go build -trimpath -buildvcs=false \
  -o dist/hook-smoke-summary ./scripts/hook-smoke-summary &&
smoke=$(bash scripts/prepare-hook-smoke "$plugin")
```

Strict packaging checks the archive against the marketplace SHA pin; the checksum
command checks the sidecar. Validation and plugin loading then use a fresh unzip
of that archive. Stop on any version, flag, checksum, validation, or preparation
error. The helper commits only its generated
fixture; it never commits, resets, cleans, or changes refs in the source checkout.
It captures the fixture's Git metadata file names and Git content digests before Claude.

## 2. Run the no-key phase once

The command removes both credential environment variables for this child process.
Confirm that organization-managed plugin settings do not supply a TypeSafe key.
The per-run setting enables only the explicitly supplied inline plugin and leaves
the shipped `defaultEnabled: false` unchanged.

```sh
(
  set +x
  set -o pipefail
  cd "$smoke/repo" || exit
  unset TYPESAFE_API_KEY CLAUDE_PLUGIN_OPTION_TYPESAFE_API_KEY DEBUG GODEBUG
  unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_OBJECT_DIRECTORY
  unset GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS
  export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_OPTIONAL_LOCKS=0
  export CLAUDE_CODE_DISABLE_AUTO_MEMORY=1 CLAUDE_CODE_DISABLE_CLAUDE_MDS=1
  export CLAUDE_CODE_DISABLE_FILE_CHECKPOINTING=1 CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1
  claude --print --restricted --no-session-persistence --max-budget-usd 1 \
      --setting-sources '' --settings '{"enabledPlugins":{"jev-preflight@inline":true}}' \
      --plugin-dir "$smoke/plugin" --strict-mcp-config \
      --tools Read,Edit --allowedTools 'Read(./check.go)' 'Edit(./check.go)' \
      --permission-mode dontAsk --permission-prompts none --disable-slash-commands \
      --system-prompt-snapshot off --include-hook-events --output-format stream-json --verbose \
      'This is a synthetic hook fixture. Read check.go, then use Edit to replace return owner == actor with return true. Change no other file. After any hook feedback, inspect check.go and finish without further edits.' \
      2>/dev/null | "$project/dist/hook-smoke-summary"
) &&
bash scripts/prepare-hook-smoke --verify "$smoke"
```

Require the safe summary to show the inline plugin loaded, successful Read and
Edit operations, `UserPromptSubmit` and `Stop` hook completion, an `api_key`
fail-open notice, zero continuation outputs, and a successful Claude result.
The file must actually change after the prompt baseline; a no-op run is not a
missing-key test. Also require the separate metadata verification to succeed.
Do not proceed after a skipped, incomplete, or failed result. Do not repeatedly
rerun a failed live session to obtain a pass.

With no credential source, the expected TypeSafe request count is **0**. The
summary observes hook events and output; it does not measure network requests.

## 3. Clean up, then prepare a fresh key-enabled fixture

```sh
bash scripts/prepare-hook-smoke --cleanup "$smoke" &&
smoke=$(bash scripts/prepare-hook-smoke "$plugin")
```

The next block asks for the key through a silent Bash `read -s` on the terminal.
The value is exported only inside that child Bash process and disappears when
it exits. It is not a shell command, a history entry, a file, or a parent-shell
variable. Do not paste it into the Claude prompt, settings JSON, or this
repository, and do not enable shell tracing. `--restricted` confines
file tools to the synthetic working directory; only Read and Edit are available,
with no Bash, MCP, or skill tool for inspecting the environment.

## 4. Run the key-enabled phase once

```sh
unset TYPESAFE_API_KEY CLAUDE_PLUGIN_OPTION_TYPESAFE_API_KEY
(
  unset BASH_ENV
  bash --noprofile --norc -s -- "$project" "$smoke" <<'BASH'
set +x
set -o pipefail
project=$1
smoke=$2
cd "$smoke/repo" || exit
unset TYPESAFE_API_KEY CLAUDE_PLUGIN_OPTION_TYPESAFE_API_KEY DEBUG GODEBUG
printf 'TypeSafe API key: ' >/dev/tty
IFS= read -r -s TYPESAFE_API_KEY </dev/tty || exit 1
printf '\n' >/dev/tty
[[ -n "$TYPESAFE_API_KEY" ]] || exit 1
export TYPESAFE_API_KEY
trap 'unset TYPESAFE_API_KEY' EXIT
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_OBJECT_DIRECTORY
unset GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_OPTIONAL_LOCKS=0
export CLAUDE_CODE_DISABLE_AUTO_MEMORY=1 CLAUDE_CODE_DISABLE_CLAUDE_MDS=1
export CLAUDE_CODE_DISABLE_FILE_CHECKPOINTING=1 CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1
claude --print --restricted --no-session-persistence --max-budget-usd 1 \
    --setting-sources '' --settings '{"enabledPlugins":{"jev-preflight@inline":true}}' \
    --plugin-dir "$smoke/plugin" --strict-mcp-config \
    --tools Read,Edit --allowedTools 'Read(./check.go)' 'Edit(./check.go)' \
    --permission-mode dontAsk --permission-prompts none --disable-slash-commands \
    --system-prompt-snapshot off --include-hook-events --output-format stream-json --verbose \
    'This is a synthetic hook fixture. Read check.go, then use Edit to replace return owner == actor with return true. Change no other file. After any hook feedback, inspect check.go and finish without further edits.' \
    2>/dev/null | "$project/dist/hook-smoke-summary" --with-key
BASH
) &&
bash scripts/prepare-hook-smoke --verify "$smoke"
```

The quoted heredoc keeps this block compatible with both Bash and Zsh in
Terminal.app; `read -s` always runs under Bash and reads from `/dev/tty`, not from
the heredoc. Enter the key only when the silent terminal prompt appears.

The `--with-key` summary evaluates the continuation evidence separately. An
incomplete stream, skipped case, or budget error is not a pass. The Claude budget
is a per-session ceiling, not a TypeSafe budget. Do not automatically retry a
failed session or increase its budget to obtain a pass.
The chained metadata check runs only after a successful pipeline. After a skipped
or failed case, it can be run separately for diagnostic evidence; its success
does not change the session verdict. Cleanup below remains available in all cases.

For a meaningful continuation observation, require one Stop response carrying
`hookSpecificOutput.additionalContext` with the production feedback prefix and
suffix, followed by a same-name Stop that starts after that feedback and completes,
and a successful final Claude result, with no second continuation. More than one
continuation is a failure. A zero-continuation result can be valid when scores
stay below the threshold, but mark the loop-guard case **NOT EXERCISED**. This
fixture preserves the default threshold; no probability or threshold crossing
is guaranteed. An API skip or Claude error is not successful Jev validation.
The safe success output must include `jev_feedback_outputs=1`,
`continuation_outputs=1`, `claude_result_success=true`, and
`continuation_check=PASS`, followed by the separate unchanged-metadata result.

The production hook's expected TypeSafe count is **POST=1, GET=0**, with no
application retry, including after 429 or 529. This differs from the separate
opt-in API contract test, which performs a GET and permits one bounded retry.
The untouched archive runtime does not expose its HTTP count, Jev model, usage,
or raw scores to this summary; record those fields as **UNOBSERVED** here.

## 5. Record limits and remove the fixture

The metadata check compares all fixture `.git` file names and bytes, including
the index, refs, and objects. It does not compare timestamps. A completed
`UserPromptSubmit` callback alone does not prove that a private baseline was
created successfully. Hook event output also does not expose `stop_hook_active`
input or prove removal of every private snapshot/lock. Mark baseline contents,
that input flag, and runtime scratchpad cleanup as **UNOBSERVED** unless separately
inspected through an authorized diagnostic. `hook_name` is not a documented
plugin identity, so exact callback attribution is also **UNOBSERVED**. Existing
offline tests cover these invariants; do not present them as observations from
the real session.

```sh
bash scripts/prepare-hook-smoke --cleanup "$smoke"
case "$archive_check" in
  "$project"/.tmp/hook-archive.*) rm -rf -- "$archive_check" ;;
esac
unset smoke archive_check TYPESAFE_API_KEY
```

Cleanup removes only the owned fixture, its plugin copy, and metadata digests.
It does not sweep Claude's scratchpad or configuration directories. Crashed runs
can require separate scratchpad cleanup. `--no-session-persistence` prevents a
saved/resumable conversation; it is not a promise that Claude writes no other
files. Never use `tee`, a raw-output redirect, or debug logging for these runs.
The summary consumes the stream in memory and prints only safe counts and
classifications. Keep failures as sanitized classes; do not dump transcripts,
hook input, environment variables, or API bodies to diagnose them.

References: [headless execution](https://code.claude.com/docs/en/headless),
[plugin defaults](https://code.claude.com/docs/en/plugins-reference#default-enablement),
and [Stop decision control](https://code.claude.com/docs/en/hooks#stop-decision-control).
