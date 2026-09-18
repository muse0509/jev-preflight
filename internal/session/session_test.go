package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testOptions(t *testing.T, fallback bool) Options {
	t.Helper()
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if err := os.Mkdir(repository, 0700); err != nil {
		t.Fatal(err)
	}
	options := Options{RepositoryRoot: repository, SessionID: "private-session-identity", PromptID: "private-prompt-identity", PluginDataDir: filepath.Join(root, "plugin-data")}
	if !fallback {
		options.ScratchpadDir = filepath.Join(root, "scratchpad")
	}
	return options
}

func mustOpen(t *testing.T, options Options) *Store {
	t.Helper()
	store, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func mustWrite(t *testing.T, path string, value []byte) {
	t.Helper()
	if err := os.WriteFile(path, value, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestStateRoundTripAndPermissions(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		name := "scratchpad"
		if fallback {
			name = "fallback"
		}
		t.Run(name, func(t *testing.T) {
			options := testOptions(t, fallback)
			store := mustOpen(t, options)
			base := filepath.Join(options.ScratchpadDir, "jev-preflight")
			if fallback {
				base = filepath.Join(options.PluginDataDir, "tmp")
			}
			canonicalBase, err := filepath.EvalSymlinks(base)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Dir(store.Dir()) != canonicalBase || !validHash(filepath.Base(store.Dir())) {
				t.Fatalf("unexpected prompt directory: %s", store.Dir())
			}
			if _, err := store.Load(); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("new store Load = %v", err)
			}
			state := store.InitialState()
			state.BaselineTree = strings.Repeat("a", 40)
			state.LastDiffHash = strings.Repeat("b", 64)
			state.NoticeClasses = []string{"api", "snapshot"}
			if err := store.Save(state); err != nil {
				t.Fatal(err)
			}
			state.ContinuationCount = 1
			if err := store.Save(state); err != nil {
				t.Fatal(err)
			}
			reopened := mustOpen(t, options)
			got, err := reopened.Load()
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, _ := json.Marshal(state)
			gotJSON, _ := json.Marshal(got)
			if !bytes.Equal(wantJSON, gotJSON) {
				t.Fatalf("state changed: %s", gotJSON)
			}
			if bytes.Contains(gotJSON, []byte(options.SessionID)) || bytes.Contains(gotJSON, []byte(options.PromptID)) {
				t.Fatal("raw session or prompt identity persisted")
			}
			entries, err := os.ReadDir(store.Dir())
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 3 {
				t.Fatalf("unexpected state files: %v", entries)
			}
			if runtime.GOOS != "windows" {
				for _, path := range []string{store.base, store.Dir(), store.SnapshotPath(), filepath.Join(store.base, ".locks"), filepath.Join(store.base, ".notices")} {
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != 0700 {
						t.Fatalf("directory permissions for %s: %v", path, err)
					}
				}
				for _, name := range []string{"state.json", ".owner.json"} {
					info, err := os.Stat(filepath.Join(store.Dir(), name))
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatalf("file permissions for %s: %v", name, err)
					}
				}
			}
		})
	}
}

func TestStateRejectsInvalidFields(t *testing.T) {
	store := mustOpen(t, testOptions(t, false))
	cases := map[string]func(*State){
		"schema":       func(s *State) { s.SchemaVersion++ },
		"repository":   func(s *State) { s.RepositoryHash = strings.Repeat("a", 64) },
		"session":      func(s *State) { s.SessionHash = strings.Repeat("a", 64) },
		"prompt":       func(s *State) { s.PromptHash = strings.Repeat("a", 64) },
		"path":         func(s *State) { s.SnapshotPath = filepath.Dir(s.SnapshotPath) },
		"negative":     func(s *State) { s.ContinuationCount = -1 },
		"over-limit":   func(s *State) { s.ContinuationCount = 2 },
		"diff-hash":    func(s *State) { s.LastDiffHash = "invalid" },
		"baseline":     func(s *State) { s.BaselineTree = "invalid" },
		"notice-body":  func(s *State) { s.NoticeClasses = []string{"error contains private text"} },
		"duplicate":    func(s *State) { s.NoticeClasses = []string{"api", "api"} },
		"many-notices": func(s *State) { s.NoticeClasses = make([]string, 33) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			state := store.InitialState()
			change(&state)
			if err := store.Save(state); err == nil {
				t.Fatal("Save accepted invalid state")
			}
			data, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(store.Dir(), "state.json"), data)
			if _, err := store.Load(); err == nil {
				t.Fatal("Load accepted invalid state")
			}
		})
	}
}

func TestStateRejectsMalformedJSON(t *testing.T) {
	store := mustOpen(t, testOptions(t, false))
	valid, _ := json.Marshal(store.InitialState())
	cases := [][]byte{[]byte(`{`), []byte(`null`), append(append([]byte{}, valid...), []byte(` {}`)...), bytes.Replace(valid, []byte(`"schemaVersion":1`), []byte(`"schemaVersion":1,"unexpected":true`), 1), bytes.Repeat([]byte(" "), maxStateBytes+1)}
	for _, input := range cases {
		mustWrite(t, filepath.Join(store.Dir(), "state.json"), input)
		if _, err := store.Load(); err == nil {
			t.Fatal("malformed JSON accepted")
		}
	}
}

func TestThreeLoopGuards(t *testing.T) {
	one, two := strings.Repeat("a", 64), strings.Repeat("b", 64)
	state := State{}
	if !state.ShouldEvaluate(one, false) || !state.CanContinue(false) {
		t.Fatal("first evaluation unexpectedly blocked")
	}
	if state.ShouldEvaluate(one, true) || state.CanContinue(true) {
		t.Fatal("active Stop hook can continue")
	}
	if state.ShouldEvaluate("", false) || state.ShouldEvaluate("invalid", false) {
		t.Fatal("invalid hash can be evaluated")
	}
	if err := state.RecordEvaluation(one, false); err != nil {
		t.Fatal(err)
	}
	if state.ShouldEvaluate(one, false) || !state.ShouldEvaluate(two, false) {
		t.Fatal("same-hash guard failed")
	}
	if err := state.RecordEvaluation(one, true); err != nil {
		t.Fatal(err)
	}
	if state.CanContinue(false) || state.ShouldEvaluate(one, false) || state.ShouldEvaluate(two, false) {
		t.Fatal("continuation-count guard failed")
	}
	if err := state.RecordEvaluation(two, true); err == nil || state.ContinuationCount != 1 || state.LastDiffHash != one {
		t.Fatal("second continuation altered state")
	}
	if err := state.RecordEvaluation("invalid", false); err == nil {
		t.Fatal("invalid hash accepted")
	}
}

func TestParallelIdentityIsolation(t *testing.T) {
	options := testOptions(t, false)
	first := mustOpen(t, options)
	if err := first.Save(first.InitialState()); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{first.Dir(): true}
	for _, pair := range [][2]string{{"same-session", "one"}, {"same-session", "two"}, {"another-session", "one"}, {"a:b", "c"}, {"a", "b:c"}, {"../../session", "../prompt"}} {
		options.SessionID, options.PromptID = pair[0], pair[1]
		store := mustOpen(t, options)
		if paths[store.Dir()] {
			t.Fatal("session identities collided")
		}
		paths[store.Dir()] = true
		state := store.InitialState()
		state.BaselineTree = strings.Repeat("c", 40)
		if err := store.Save(state); err != nil {
			t.Fatal(err)
		}
	}
	state, err := first.Load()
	if err != nil || state.BaselineTree != "" {
		t.Fatal("another prompt changed first state")
	}
	secondRepository := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(secondRepository, 0700); err != nil {
		t.Fatal(err)
	}
	options.RepositoryRoot = secondRepository
	if paths[mustOpen(t, options).Dir()] {
		t.Fatal("repositories collided")
	}
}

func TestLockContentionAndCleanup(t *testing.T) {
	options := testOptions(t, false)
	store := mustOpen(t, options)
	other := mustOpen(t, options)
	unlock, acquired, err := store.Lock()
	if err != nil || !acquired {
		t.Fatalf("initial lock: %v", err)
	}
	defer unlock()
	if _, acquired, err := other.Lock(); err != nil || acquired {
		t.Fatalf("contended lock: acquired=%v err=%v", acquired, err)
	}
	if err := store.Save(store.InitialState()); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(store.SnapshotPath(), "private-object"), []byte("ephemeral snapshot"))
	if err := store.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Dir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("prompt state was not removed")
	}
	if _, acquired, err := other.Lock(); err != nil || acquired {
		t.Fatal("cleanup released a held lock")
	}
	unlock()
	unlockAgain, acquired, err := other.Lock()
	if err != nil || !acquired {
		t.Fatalf("lock after release: %v", err)
	}
	unlockAgain()
	if err := store.Cleanup(); err != nil {
		t.Fatalf("repeated cleanup: %v", err)
	}
	reopened := mustOpen(t, options)
	if _, err := reopened.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("finalized prompt retained baseline")
	}
}

func TestConcurrentLockHasOneWinner(t *testing.T) {
	store := mustOpen(t, testOptions(t, false))
	var winners atomic.Int32
	var waiting sync.WaitGroup
	var finished sync.WaitGroup
	start, release := make(chan struct{}), make(chan struct{})
	for i := 0; i < 24; i++ {
		waiting.Add(1)
		finished.Add(1)
		go func() {
			defer finished.Done()
			<-start
			unlock, acquired, err := store.Lock()
			if err != nil {
				t.Errorf("lock: %v", err)
			}
			if acquired {
				winners.Add(1)
			}
			waiting.Done()
			if acquired {
				<-release
				unlock()
			}
		}()
	}
	close(start)
	waiting.Wait()
	if got := winners.Load(); got != 1 {
		t.Fatalf("lock winners = %d", got)
	}
	close(release)
	finished.Wait()
}

func TestNoticesPersistAcrossPromptsAndCleanup(t *testing.T) {
	options := testOptions(t, false)
	first := mustOpen(t, options)
	if emitted, err := first.Notice("api"); err != nil || !emitted {
		t.Fatalf("first notice = %v, %v", emitted, err)
	}
	if emitted, err := first.Notice("api"); err != nil || emitted {
		t.Fatalf("duplicate notice = %v, %v", emitted, err)
	}
	if err := first.Cleanup(); err != nil {
		t.Fatal(err)
	}
	options.PromptID = "another-prompt"
	next := mustOpen(t, options)
	if emitted, err := next.Notice("api"); err != nil || emitted {
		t.Fatalf("cross-prompt duplicate = %v, %v", emitted, err)
	}
	if emitted, err := next.Notice("snapshot"); err != nil || !emitted {
		t.Fatalf("different class = %v, %v", emitted, err)
	}
	options.SessionID = "another-session"
	separate := mustOpen(t, options)
	if emitted, err := separate.Notice("api"); err != nil || !emitted {
		t.Fatalf("different session = %v, %v", emitted, err)
	}
	if emitted, err := separate.Notice("error with private body"); err == nil || emitted {
		t.Fatal("notice accepted arbitrary text")
	}
	data, err := os.ReadFile(filepath.Join(next.base, ".notices", next.owner.SessionHash+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("private-session-identity")) || bytes.Contains(data, []byte("another-prompt")) {
		t.Fatal("notice ledger persisted raw identity")
	}
}

func TestConcurrentNoticeEmitsAtMostOnce(t *testing.T) {
	store := mustOpen(t, testOptions(t, false))
	var emitted atomic.Int32
	var group sync.WaitGroup
	for i := 0; i < 24; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			yes, err := store.Notice("api")
			if err != nil {
				t.Errorf("notice: %v", err)
			}
			if yes {
				emitted.Add(1)
			}
		}()
	}
	group.Wait()
	if emitted.Load() != 1 {
		t.Fatalf("notice emissions = %d", emitted.Load())
	}
}

func TestFallbackTTLOnlyRemovesOwnedExpiredUnlockedDirectories(t *testing.T) {
	options := testOptions(t, true)
	expired := mustOpen(t, options)
	options.PromptID = "fresh"
	fresh := mustOpen(t, options)
	options.PromptID = "locked"
	locked := mustOpen(t, options)
	options.PromptID = "updated"
	updated := mustOpen(t, options)
	for _, store := range []*Store{expired, fresh, locked, updated} {
		if err := store.Save(store.InitialState()); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-FallbackTTL - time.Hour)
	for _, store := range []*Store{expired, locked, updated} {
		for _, path := range []string{store.Dir(), filepath.Join(store.Dir(), ".owner.json"), filepath.Join(store.Dir(), "state.json")} {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Chtimes(filepath.Join(updated.Dir(), "state.json"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	unrecognized := filepath.Join(expired.base, strings.Repeat("e", 64))
	if err := os.Mkdir(unrecognized, 0700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(unrecognized, "preserve"), []byte("user content"))
	if err := os.Chtimes(unrecognized, old, old); err != nil {
		t.Fatal(err)
	}
	unlock, acquired, err := locked.Lock()
	if err != nil || !acquired {
		t.Fatal("lock failed")
	}
	defer unlock()
	if err := fresh.CleanupExpired(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expired.Dir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired owned directory remains")
	}
	for _, path := range []string{fresh.Dir(), locked.Dir(), updated.Dir(), unrecognized} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("noneligible path removed: %s: %v", path, err)
		}
	}
}

func TestFallbackTTLCleanupOnOpen(t *testing.T) {
	options := testOptions(t, true)
	expired := mustOpen(t, options)
	old := time.Now().Add(-2 * FallbackTTL)
	for _, path := range []string{filepath.Join(expired.Dir(), ".owner.json"), expired.Dir()} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	options.PromptID = "fresh"
	mustOpen(t, options)
	if _, err := os.Stat(expired.Dir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Open did not clean expired fallback state")
	}
}

func TestScratchpadHasNoTTLCleanup(t *testing.T) {
	store := mustOpen(t, testOptions(t, false))
	if err := store.CleanupExpired(time.Now().Add(365 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Dir()); err != nil {
		t.Fatal("scratchpad directory was TTL-cleaned")
	}
}

func TestFallbackTTLRemovesPrivateDataBehindAbandonedLock(t *testing.T) {
	options := testOptions(t, true)
	store := mustOpen(t, options)
	if err := store.Save(store.InitialState()); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(store.SnapshotPath(), "private-object"), []byte("ephemeral private data"))
	unlock, acquired, err := store.Lock()
	if err != nil || !acquired {
		t.Fatal("lock failed")
	}
	defer unlock()
	old := time.Now().Add(-2 * FallbackTTL)
	lock := filepath.Join(store.base, ".locks", store.owner.PromptHash)
	for _, path := range []string{store.Dir(), filepath.Join(store.Dir(), ".owner.json"), filepath.Join(store.Dir(), "state.json"), lock} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.CleanupExpired(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Dir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("abandoned private data remains")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("crash tombstone was removed")
	}
	if _, acquired, err := store.Lock(); err != nil || acquired {
		t.Fatal("abandoned prompt can replay")
	}
}

func TestNoticeRejectsCorruptLedger(t *testing.T) {
	store := mustOpen(t, testOptions(t, false))
	ledger := filepath.Join(store.base, ".notices", store.owner.SessionHash+".json")
	for _, body := range []string{"null", "{}", "{", `{"schemaVersion":2}`} {
		mustWrite(t, ledger, []byte(body))
		if emit, err := store.Notice("api"); err == nil || emit {
			t.Fatal("corrupt ledger reset notice guard")
		}
	}
}

func symlinkOrSkip(t *testing.T, old, new string) {
	t.Helper()
	if err := os.Symlink(old, new); err != nil {
		t.Skipf("symlink capability unavailable: %v", err)
	}
}

func TestSymlinkDestinationsAreRejected(t *testing.T) {
	for _, kind := range []string{"base", "prompt", "snapshot", "state", "owner", "notice"} {
		t.Run(kind, func(t *testing.T) {
			options := testOptions(t, false)
			store := mustOpen(t, options)
			victim := t.TempDir()
			victimFile := filepath.Join(victim, "preserve")
			mustWrite(t, victimFile, []byte("must remain unchanged"))
			path, target := store.base, victim
			switch kind {
			case "prompt":
				path = store.Dir()
			case "snapshot":
				path = store.SnapshotPath()
			case "state":
				path, target = filepath.Join(store.Dir(), "state.json"), victimFile
			case "owner":
				path, target = filepath.Join(store.Dir(), ".owner.json"), victimFile
			case "notice":
				path, target = filepath.Join(store.base, ".notices", store.owner.SessionHash+".json"), victimFile
			}
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			symlinkOrSkip(t, target, path)
			if kind == "notice" {
				if emit, err := store.Notice("api"); err == nil || emit {
					t.Fatal("symlink notice accepted")
				}
			} else {
				if err := store.Save(store.InitialState()); err == nil {
					t.Fatal("symlink state destination accepted")
				}
				if kind != "state" {
					if _, err := Open(options); err == nil {
						t.Fatal("symlink storage directory accepted")
					}
					if err := store.Cleanup(); err == nil {
						t.Fatal("cleanup accepted unsafe directory")
					}
				}
			}
			data, err := os.ReadFile(victimFile)
			if err != nil || string(data) != "must remain unchanged" {
				t.Fatal("symlink target changed")
			}
		})
	}
}

func TestCleanupDoesNotFollowNestedSnapshotSymlink(t *testing.T) {
	store := mustOpen(t, testOptions(t, false))
	victim := t.TempDir()
	mustWrite(t, filepath.Join(victim, "preserve"), []byte("preserve"))
	symlinkOrSkip(t, victim, filepath.Join(store.SnapshotPath(), "nested"))
	if err := store.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(victim, "preserve")); err != nil {
		t.Fatal("cleanup followed snapshot symlink")
	}
}

func TestTTLDoesNotFollowHashedSymlink(t *testing.T) {
	store := mustOpen(t, testOptions(t, true))
	victim := t.TempDir()
	mustWrite(t, filepath.Join(victim, "preserve"), []byte("preserve"))
	link := filepath.Join(store.base, strings.Repeat("f", 64))
	symlinkOrSkip(t, victim, link)
	if err := store.CleanupExpired(time.Now().Add(2 * FallbackTTL)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal("TTL deleted unowned symlink")
	}
	if _, err := os.Stat(filepath.Join(victim, "preserve")); err != nil {
		t.Fatal("TTL followed symlink")
	}
}

func TestOpenRejectsInvalidInputs(t *testing.T) {
	for _, change := range []func(*Options){
		func(o *Options) { o.SessionID = "" },
		func(o *Options) { o.PromptID = "" },
		func(o *Options) { o.RepositoryRoot = "relative" },
		func(o *Options) { o.RepositoryRoot = filepath.Join(o.RepositoryRoot, "absent") },
		func(o *Options) { o.ScratchpadDir = "relative" },
		func(o *Options) { o.ScratchpadDir, o.PluginDataDir = "", "" },
	} {
		options := testOptions(t, false)
		change(&options)
		if _, err := Open(options); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
}

func TestOpenDoesNotAdoptUnownedData(t *testing.T) {
	options := testOptions(t, false)
	store := mustOpen(t, options)
	if err := os.Remove(filepath.Join(store.Dir(), ".owner.json")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(store.Dir(), "preserve"), []byte("preserve"))
	if _, err := Open(options); err == nil {
		t.Fatal("adopted an existing unowned directory")
	}
	if err := store.Cleanup(); err == nil {
		t.Fatal("removed unowned directory")
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), "preserve")); err != nil {
		t.Fatal("unowned data removed")
	}
}
