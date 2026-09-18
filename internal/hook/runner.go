package hook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/muse0509/jev-preflight/internal/config"
	turnDiff "github.com/muse0509/jev-preflight/internal/diff"
	"github.com/muse0509/jev-preflight/internal/gitstate"
	"github.com/muse0509/jev-preflight/internal/jev"
	"github.com/muse0509/jev-preflight/internal/policy"
	"github.com/muse0509/jev-preflight/internal/session"
)

// Runner permits a fake API client at the network boundary. Production fixes its endpoint.
type Runner struct {
	Client *jev.Client
	Getenv func(string) string
	Stderr io.Writer
}

func (r Runner) Run(ctx context.Context, input io.Reader, event string) Output {
	getenv := r.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	in, err := Decode(input, event)
	if err != nil {
		return r.failure(nil, "input")
	}
	repo, err := gitstate.Resolve(ctx, in.CWD)
	if err != nil {
		return r.failure(nil, "repository")
	}
	store, err := session.Open(session.Options{RepositoryRoot: repo.Root, SessionID: in.SessionID, PromptID: in.PromptID, ScratchpadDir: in.ScratchpadDir, PluginDataDir: getenv("CLAUDE_PLUGIN_DATA")})
	if err != nil {
		return r.failure(nil, "storage")
	}
	unlock, acquired, err := store.Lock()
	if err != nil {
		return r.failure(store, "storage")
	}
	if !acquired {
		return Output{}
	}
	defer unlock()
	keep := false
	defer func() {
		if !keep {
			if store.Cleanup() != nil {
				r.diagnostic("cleanup")
			}
		}
	}()
	// In-flight tasks and scheduled wakeups keep the original turn baseline.
	if event == "Stop" && in.HasBackgroundWork() {
		keep = true
		return Output{}
	}
	st, stateErr := store.Load()
	if event == "Stop" {
		if errors.Is(stateErr, os.ErrNotExist) {
			return Output{}
		}
		if stateErr != nil {
			return r.failure(store, "state")
		}
		if !st.CanContinue(*in.StopHookActive) {
			return Output{}
		}
	} else if stateErr == nil {
		// Duplicate submissions cannot move the baseline forward.
		keep = true
		return Output{}
	} else if !errors.Is(stateErr, os.ErrNotExist) {
		return r.failure(store, "state")
	}
	cfg, err := config.Load(repo.Root)
	if err != nil {
		return r.failure(store, "config")
	}
	if cfg.Mode == "off" {
		return Output{}
	}
	pol, err := policy.Load()
	if err != nil {
		return r.failure(store, "policy")
	}
	if event == "UserPromptSubmit" {
		tree, err := repo.Snapshot(ctx, store.SnapshotPath())
		if err != nil {
			return r.failure(store, "snapshot")
		}
		st = store.InitialState()
		st.BaselineTree = tree
		if store.Save(st) != nil {
			return r.failure(store, "state")
		}
		keep = true
		return Output{}
	}
	current, err := repo.Snapshot(ctx, store.SnapshotPath())
	if err != nil {
		return r.failure(store, "snapshot")
	}
	changes, err := repo.Diff(ctx, store.SnapshotPath(), st.BaselineTree, current)
	if errors.Is(err, gitstate.ErrOutputTooLarge) {
		return r.failure(store, "diff_too_large")
	}
	if err != nil {
		return r.failure(store, "diff")
	}
	normalized, err := turnDiff.Prepare(changes, cfg.Exclude, cfg.MaxDiffBytes)
	if errors.Is(err, turnDiff.ErrTooLarge) {
		return r.failure(store, "diff_too_large")
	}
	if err != nil {
		return r.failure(store, "diff")
	}
	if len(normalized.State.Files) == 0 || !st.ShouldEvaluate(normalized.Hash, *in.StopHookActive) {
		return Output{}
	}
	key := config.APIKey(getenv)
	if key == "" {
		return r.failure(store, "api_key")
	}
	// Persist before calling out, so an interrupted evaluation is not retried.
	if st.RecordEvaluation(normalized.Hash, false) != nil || store.Save(st) != nil {
		return r.failure(store, "state")
	}
	client := r.Client
	if client == nil {
		client = jev.New(jev.Endpoint, nil)
	}
	apiCtx, apiCancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
	defer apiCancel()
	scores, err := client.Evaluate(apiCtx, key, normalized.State, pol.Questions)
	if err != nil {
		var apiError *jev.Error
		if errors.As(err, &apiError) {
			return r.failure(store, "api_"+apiError.Class)
		}
		return r.failure(store, "api")
	}
	risks := policy.Select(scores, cfg.RiskThreshold)
	if len(risks) == 0 {
		return Output{}
	}
	if cfg.Mode == "report" {
		message := "jev-preflight report: investigation priorities (not proof of defects):"
		for _, risk := range risks {
			message += fmt.Sprintf(" %s=%.3f", risk.ID, risk.Probability)
		}
		return Output{SystemMessage: message}
	}
	feedback := policy.Feedback(risks, normalized.State.Files)
	if st.RecordEvaluation(normalized.Hash, true) != nil || store.Save(st) != nil {
		return r.failure(store, "state")
	}
	keep = true
	return Output{Specific: &SpecificOutput{HookEventName: "Stop", AdditionalContext: feedback}}
}

func (r Runner) diagnostic(class string) {
	if r.Stderr != nil {
		_, _ = io.WriteString(r.Stderr, "jev-preflight: skipped ("+class+").\n")
	}
}

func (r Runner) failure(store *session.Store, class string) Output {
	r.diagnostic(class)
	// Without a valid session store there is no safe way to deduplicate notices.
	if store == nil {
		return Output{}
	}
	emit, err := store.Notice(class)
	if err != nil || !emit {
		return Output{}
	}
	message := "jev-preflight: skipped (" + class + "); Claude may finish."
	if class == "diff_too_large" {
		message = "jev-preflight: skipped: diff too large; Claude may finish."
	}
	return Output{SystemMessage: message}
}
