// Package session keeps private prompt snapshots and body-free loop state.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	SchemaVersion = 1
	FallbackTTL   = 24 * time.Hour
	maxStateBytes = 16384
	ownerFormat   = "jev-preflight/session/v1"
)

// Options contains identities only; never pass prompt or assistant text.
type Options struct {
	RepositoryRoot string
	SessionID      string
	PromptID       string
	ScratchpadDir  string
	PluginDataDir  string
}

type State struct {
	SchemaVersion     int      `json:"schemaVersion"`
	RepositoryHash    string   `json:"repositoryHash"`
	SessionHash       string   `json:"sessionHash"`
	PromptHash        string   `json:"promptHash"`
	SnapshotPath      string   `json:"snapshotPath"`
	BaselineTree      string   `json:"baselineTree"`
	ContinuationCount int      `json:"continuationCount"`
	LastDiffHash      string   `json:"lastDiffHash"`
	NoticeClasses     []string `json:"noticeClasses"`
}

type ownership struct {
	Format         string `json:"format"`
	RepositoryHash string `json:"repositoryHash"`
	SessionHash    string `json:"sessionHash"`
	PromptHash     string `json:"promptHash"`
}

type noticeLedger struct {
	SchemaVersion  int      `json:"schemaVersion"`
	RepositoryHash string   `json:"repositoryHash"`
	SessionHash    string   `json:"sessionHash"`
	NoticeClasses  []string `json:"noticeClasses"`
}

type Store struct {
	base     string
	dir      string
	owner    ownership
	fallback bool
}

// Open derives opaque paths and checks ownership before reusing a directory.
func Open(options Options) (*Store, error) {
	if options.SessionID == "" || options.PromptID == "" {
		return nil, errors.New("missing session identity")
	}
	repository, err := canonicalDirectory(options.RepositoryRoot, false)
	if err != nil {
		return nil, errors.New("invalid repository directory")
	}
	root, child := options.ScratchpadDir, "jev-preflight"
	fallback := root == ""
	if fallback {
		root, child = options.PluginDataDir, "tmp"
	}
	root, err = canonicalDirectory(root, true)
	if err != nil {
		return nil, errors.New("invalid session storage directory")
	}
	s := &Store{base: filepath.Join(root, child), fallback: fallback}
	s.owner = ownership{
		Format:         ownerFormat,
		RepositoryHash: identityHash("repository", repository),
		SessionHash:    identityHash("session", repository, options.SessionID),
		PromptHash:     identityHash("prompt", repository, options.SessionID, options.PromptID),
	}
	s.dir = filepath.Join(s.base, s.owner.PromptHash)
	for _, directory := range []string{s.base, filepath.Join(s.base, ".locks"), filepath.Join(s.base, ".notices")} {
		if err := ensureDirectory(directory); err != nil {
			return nil, err
		}
	}
	if fallback {
		if err := s.CleanupExpired(time.Now()); err != nil {
			return nil, err
		}
	}
	if err := ensureDirectory(s.dir); err != nil {
		return nil, err
	}
	marker := filepath.Join(s.dir, ".owner.json")
	var existing ownership
	if err := readJSON(marker, &existing); errors.Is(err, os.ErrNotExist) {
		entries, err := os.ReadDir(s.dir)
		if err != nil || len(entries) != 0 {
			return nil, errors.New("unrecognized session directory")
		}
		if err := createOwnership(marker, s.owner); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := s.validatePrompt(); err != nil {
		return nil, err
	}
	if err := ensureDirectory(s.SnapshotPath()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Dir() string          { return s.dir }
func (s *Store) SnapshotPath() string { return filepath.Join(s.dir, "snapshot") }

func (s *Store) InitialState() State {
	return State{
		SchemaVersion:  SchemaVersion,
		RepositoryHash: s.owner.RepositoryHash,
		SessionHash:    s.owner.SessionHash,
		PromptHash:     s.owner.PromptHash,
		SnapshotPath:   s.SnapshotPath(),
		NoticeClasses:  []string{},
	}
}

func (s *Store) Load() (State, error) {
	var state State
	if err := s.validatePrompt(); err != nil {
		return state, err
	}
	if err := readJSON(filepath.Join(s.dir, "state.json"), &state); err != nil {
		return state, err
	}
	if err := s.validateState(state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (s *Store) Save(state State) error {
	if err := s.validatePrompt(); err != nil {
		return err
	}
	if err := s.validateState(state); err != nil {
		return err
	}
	return atomicJSON(s.dir, "state.json", state)
}

// Lock never waits. The caller must hold it across evaluation and cleanup.
func (s *Store) Lock() (unlock func(), acquired bool, err error) {
	if err := s.validateBase(); err != nil {
		return nil, false, err
	}
	path := filepath.Join(s.base, ".locks", s.owner.PromptHash)
	unlock, acquired, err = acquireDirectoryLock(path)
	if err != nil {
		return nil, false, errors.New("cannot acquire session lock")
	}
	return unlock, acquired, nil
}

func acquireDirectoryLock(path string) (unlock func(), acquired bool, err error) {
	if err := os.Mkdir(path, 0700); errors.Is(err, os.ErrExist) {
		return func() {}, false, nil
	} else if err != nil {
		return nil, false, err
	}
	// File.Stat captures the Windows file identity before the name is reused.
	directory, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	info, statErr := directory.Stat()
	closeErr := directory.Close()
	if statErr != nil {
		return nil, false, statErr
	}
	if closeErr != nil {
		return nil, false, closeErr
	}
	var once sync.Once
	return func() {
		once.Do(func() { releaseDirectoryLock(path, info) })
	}, true, nil
}

func releaseDirectoryLock(path string, owner os.FileInfo) {
	current, err := os.Lstat(path)
	if err != nil || !current.IsDir() || !os.SameFile(owner, current) {
		return
	}
	// Windows deletion can wait for open handles. Retire the name first so a
	// pending deletion cannot prevent another process from acquiring the lock.
	var identity [16]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return
	}
	destination := filepath.Join(filepath.Dir(path), ".released-"+hex.EncodeToString(identity[:]))
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := os.Rename(path, destination); err != nil {
		return
	}
	_ = os.Remove(destination)
}

// Cleanup removes only the positively identified prompt directory.
func (s *Store) Cleanup() error {
	if err := s.validateBase(); err != nil {
		return err
	}
	if _, err := os.Lstat(s.dir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := s.validatePrompt(); err != nil {
		return err
	}
	return os.RemoveAll(s.dir)
}

// Notice persists error classes across prompts without storing error details.
func (s *Store) Notice(class string) (bool, error) {
	if !validClass(class) {
		return false, errors.New("invalid notice class")
	}
	if err := s.validateBase(); err != nil {
		return false, err
	}
	directory := filepath.Join(s.base, ".notices")
	lock := filepath.Join(directory, s.owner.SessionHash+".lock")
	unlock, acquired, err := acquireDirectoryLock(lock)
	if err != nil {
		return false, errors.New("cannot acquire notice lock")
	}
	if !acquired {
		return false, nil
	}
	defer unlock()
	var ledger noticeLedger
	name := s.owner.SessionHash + ".json"
	if err := readJSON(filepath.Join(directory, name), &ledger); errors.Is(err, os.ErrNotExist) {
		ledger = noticeLedger{SchemaVersion: SchemaVersion, RepositoryHash: s.owner.RepositoryHash, SessionHash: s.owner.SessionHash}
	} else if err != nil {
		return false, err
	}
	if ledger.SchemaVersion != SchemaVersion || ledger.RepositoryHash != s.owner.RepositoryHash || ledger.SessionHash != s.owner.SessionHash || !validClasses(ledger.NoticeClasses) {
		return false, errors.New("invalid notice ledger")
	}
	for _, previous := range ledger.NoticeClasses {
		if previous == class {
			return false, nil
		}
	}
	if len(ledger.NoticeClasses) >= 32 {
		return false, errors.New("too many notice classes")
	}
	ledger.NoticeClasses = append(ledger.NoticeClasses, class)
	sort.Strings(ledger.NoticeClasses)
	if err := atomicJSON(directory, name, ledger); err != nil {
		return false, err
	}
	return true, nil
}

// CleanupExpired bounds work and preserves recently locked state.
func (s *Store) CleanupExpired(now time.Time) error {
	if !s.fallback {
		return nil
	}
	if err := s.validateBase(); err != nil {
		return err
	}
	directory, err := os.Open(s.base)
	if err != nil {
		return errors.New("cannot inspect fallback storage")
	}
	defer directory.Close()
	entries, err := directory.ReadDir(512)
	if err != nil && !errors.Is(err, io.EOF) {
		return errors.New("cannot inspect fallback storage")
	}
	for _, entry := range entries {
		if !validHash(entry.Name()) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		candidate := filepath.Join(s.base, entry.Name())
		var owner ownership
		if readJSON(filepath.Join(candidate, ".owner.json"), &owner) != nil || owner.Format != ownerFormat || owner.PromptHash != entry.Name() || !validHash(owner.RepositoryHash) || !validHash(owner.SessionHash) {
			continue
		}
		latest := time.Time{}
		for _, path := range []string{candidate, filepath.Join(candidate, ".owner.json"), filepath.Join(candidate, "state.json")} {
			if info, err := os.Lstat(path); err == nil && info.ModTime().After(latest) {
				latest = info.ModTime()
			}
		}
		if now.Sub(latest) < FallbackTTL {
			continue
		}
		other := &Store{base: s.base, dir: candidate, owner: owner, fallback: true}
		unlock, acquired, err := other.Lock()
		if err != nil {
			continue
		}
		if !acquired {
			// Hooks are bounded. An expired lock is a crash tombstone: remove
			// private data but retain the lock so the old prompt cannot replay.
			lock, err := os.Lstat(filepath.Join(s.base, ".locks", owner.PromptHash))
			if err != nil || !lock.IsDir() || now.Sub(lock.ModTime()) < FallbackTTL {
				continue
			}
			if err := other.Cleanup(); err != nil {
				return errors.New("cannot clean abandoned session")
			}
			continue
		}
		err = other.Cleanup()
		unlock()
		if err != nil {
			return errors.New("cannot clean expired session")
		}
	}
	return nil
}

func (state State) CanContinue(stopHookActive bool) bool {
	return !stopHookActive && state.ContinuationCount == 0
}

func (state State) ShouldEvaluate(hash string, stopHookActive bool) bool {
	return state.CanContinue(stopHookActive) && validHash(hash) && state.LastDiffHash != hash
}

func (state *State) RecordEvaluation(hash string, continuation bool) error {
	if !validHash(hash) || state.ContinuationCount < 0 || state.ContinuationCount > 1 || (continuation && state.ContinuationCount != 0) {
		return errors.New("invalid loop state transition")
	}
	state.LastDiffHash = hash
	if continuation {
		state.ContinuationCount++
	}
	return nil
}

func (s *Store) validateBase() error {
	for _, path := range []string{s.base, filepath.Join(s.base, ".locks"), filepath.Join(s.base, ".notices")} {
		if err := checkDirectory(path); err != nil {
			return err
		}
	}
	resolved, err := filepath.EvalSymlinks(s.base)
	if err != nil || resolved != s.base {
		return errors.New("unsafe session storage path")
	}
	return nil
}

func (s *Store) validatePrompt() error {
	if err := s.validateBase(); err != nil {
		return err
	}
	if !validHash(filepath.Base(s.dir)) || filepath.Dir(s.dir) != s.base || filepath.Base(s.dir) != s.owner.PromptHash {
		return errors.New("unsafe prompt path")
	}
	if err := checkDirectory(s.dir); err != nil {
		return err
	}
	var owner ownership
	if err := readJSON(filepath.Join(s.dir, ".owner.json"), &owner); err != nil {
		return err
	}
	if owner != s.owner {
		return errors.New("session ownership mismatch")
	}
	if err := checkDirectory(s.SnapshotPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) validateState(state State) error {
	if state.SchemaVersion != SchemaVersion || state.RepositoryHash != s.owner.RepositoryHash || state.SessionHash != s.owner.SessionHash || state.PromptHash != s.owner.PromptHash || state.SnapshotPath != s.SnapshotPath() || state.ContinuationCount < 0 || state.ContinuationCount > 1 || !validClasses(state.NoticeClasses) {
		return errors.New("invalid session state")
	}
	if state.LastDiffHash != "" && !validHash(state.LastDiffHash) {
		return errors.New("invalid diff identity")
	}
	if state.BaselineTree != "" && !validHex(state.BaselineTree, 40) && !validHex(state.BaselineTree, 64) {
		return errors.New("invalid baseline tree identity")
	}
	return nil
}

func canonicalDirectory(path string, create bool) (string, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return "", errors.New("directory must be absolute")
	}
	if create {
		if err := os.MkdirAll(path, 0700); err != nil {
			return "", err
		}
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if err := checkDirectory(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func ensureDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("cannot create private directory")
	}
	if err := checkDirectory(path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0700); err != nil {
		return errors.New("cannot secure private directory")
	}
	return nil
}

func checkDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe private directory")
	}
	return nil
}

func identityHash(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		fmt.Fprintf(digest, "%d:", len(part))
		digest.Write([]byte(part))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func validHash(value string) bool { return validHex(value, 64) }

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'f') && !(char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func validClass(class string) bool {
	if len(class) == 0 || len(class) > 64 {
		return false
	}
	for _, char := range class {
		if !(char >= 'a' && char <= 'z') && !(char >= '0' && char <= '9') && char != '_' && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func validClasses(classes []string) bool {
	if len(classes) > 32 {
		return false
	}
	seen := make(map[string]bool, len(classes))
	for _, class := range classes {
		if !validClass(class) || seen[class] {
			return false
		}
		seen[class] = true
	}
	return true
}

func createOwnership(path string, owner ownership) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(file).Encode(owner)
	closeErr := file.Close()
	if encodeErr != nil || closeErr != nil {
		return errors.New("cannot initialize session ownership")
	}
	return nil
}

func readJSON(path string, destination any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxStateBytes || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return errors.New("unsafe session state file")
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("cannot read session state")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("session state changed while opening")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxStateBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("invalid session state JSON")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("invalid session state JSON")
	}
	return nil
}

func atomicJSON(directory, name string, value any) error {
	path := filepath.Join(directory, name)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("unsafe session state destination")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect session state destination")
	}
	file, err := os.CreateTemp(directory, ".state-*")
	if err != nil {
		return errors.New("cannot create temporary session state")
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return errors.New("cannot secure session state")
	}
	if err := json.NewEncoder(file).Encode(value); err != nil {
		file.Close()
		return errors.New("cannot encode session state")
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return errors.New("cannot synchronize session state")
	}
	if err := file.Close(); err != nil {
		return errors.New("cannot close session state")
	}
	if err := os.Rename(temporary, path); err != nil {
		return errors.New("cannot replace session state")
	}
	return nil
}
