// Package diff selects and normalizes turn changes before transmission.
package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/muse0509/jev-preflight/internal/gitstate"
	"github.com/muse0509/jev-preflight/internal/jev"
	"github.com/muse0509/jev-preflight/internal/redact"
)

var ErrTooLarge = errors.New("diff too large")

type Result struct {
	State jev.State
	Hash  string
}

// Prepare hashes the exact canonical UTF-8 representation sent as state.
// Oversized changes are skipped as a whole, never truncated or batched.
func Prepare(changes []gitstate.Change, exclude []string, maxBytes int) (Result, error) {
	ordered := append([]gitstate.Change(nil), changes...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Path == ordered[j].Path {
			return ordered[i].OldPath < ordered[j].OldPath
		}
		return ordered[i].Path < ordered[j].Path
	})
	state := jev.State{Format: "unified_diff", Files: []string{}}
	files := make(map[string]bool)
	var patches strings.Builder
	for _, c := range ordered {
		// A rename crossing an excluded boundary must not reveal excluded content.
		if !Included(c.Path, exclude) || (c.OldPath != "" && !Included(c.OldPath, exclude)) {
			continue
		}
		body := c.Patch
		if c.Binary {
			meta := struct {
				Path    string `json:"path"`
				OldPath string `json:"old_path,omitempty"`
				Status  string `json:"status"`
			}{c.Path, c.OldPath, c.Status}
			encoded, _ := json.Marshal(meta)
			body = "Binary change: " + string(encoded) + "\n"
		}
		if strings.TrimSpace(body) == "" {
			continue
		}
		body = redact.Text(strings.ToValidUTF8(strings.ReplaceAll(body, "\r\n", "\n"), "\uFFFD"))
		patches.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			patches.WriteByte('\n')
		}
		files[redact.Text(c.Path)] = true
		if c.OldPath != "" {
			files[redact.Text(c.OldPath)] = true
		}
	}
	for file := range files {
		state.Files = append(state.Files, file)
	}
	sort.Strings(state.Files)
	state.Diff = patches.String()
	if len(state.Files) == 0 {
		return Result{State: state}, nil
	}
	canonical, err := json.Marshal(state)
	if err != nil {
		return Result{}, errors.New("invalid normalized diff")
	}
	if len(canonical) > maxBytes {
		return Result{}, ErrTooLarge
	}
	hash := sha256.Sum256(canonical)
	return Result{State: state, Hash: hex.EncodeToString(hash[:])}, nil
}

// Included is deliberately a small path policy, not a language detector.
func Included(file string, exclude []string) bool {
	if file == "" || !utf8.ValidString(file) || strings.ContainsRune(file, 0) || path.IsAbs(file) || path.Clean(file) != file || file == ".." || strings.HasPrefix(file, "../") {
		return false
	}
	for _, pattern := range exclude {
		if Match(pattern, file) {
			return false
		}
	}
	lower := strings.ToLower(file)
	for _, part := range strings.Split(lower, "/") {
		switch part {
		case ".git", "dist", "build", "target", "out", "coverage", ".tmp", "vendor", "node_modules", "generated", "__pycache__":
			return false
		}
	}
	base := path.Base(lower)
	// These .txt files affect runtime dependencies or builds, not documentation.
	if base == "requirements.txt" || base == "cmakelists.txt" || (strings.HasPrefix(base, "requirements-") && strings.HasSuffix(base, ".txt")) || (strings.HasPrefix(lower, "requirements/") && strings.HasSuffix(base, ".txt")) {
		return true
	}
	switch base {
	case "go.sum", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb", "cargo.lock", "gemfile.lock", "poetry.lock", "uv.lock", "composer.lock", "packages.lock.json", "license", "licence", "copying", "notice":
		return false
	}
	if strings.HasSuffix(base, ".lock") || strings.Contains(base, ".min.") || strings.Contains(base, ".generated.") || strings.Contains(base, ".gen.") || strings.Contains(base, "_generated.") || strings.HasSuffix(base, ".pb.go") {
		return false
	}
	switch path.Ext(lower) {
	case ".md", ".mdx", ".rst", ".txt", ".adoc", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".svg", ".pdf", ".mp3", ".mp4", ".wav", ".mov", ".woff", ".woff2", ".ttf":
		return false
	}
	return true
}

// Match uses slash-separated repository paths. A final /** includes descendants.
func Match(pattern, file string) bool {
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		parts := strings.Split(file, "/")
		for n := 1; n <= len(parts); n++ {
			if ok, _ := path.Match(prefix, strings.Join(parts[:n], "/")); ok {
				return true
			}
		}
		return false
	}
	ok, _ := path.Match(pattern, file)
	return ok
}
