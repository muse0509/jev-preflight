package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := unpinnedFixture(t)
	result, err := buildArchive(root, "v0.1.0", true)
	if err != nil {
		t.Fatal(err)
	}
	setMarketplacePin(t, root, &result.SHA256)
	return root
}

func unpinnedFixture(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range sourcePaths {
		body := []byte("fixture: " + name + "\n")
		if name == ".claude-plugin/plugin.json" {
			body = []byte(`{"name":"jev-preflight","version":"0.1.0"}`)
		}
		writeFixture(t, root, name, body)
	}
	writeFixture(t, root, ".claude-plugin/marketplace.json", []byte(`{"plugins":[{"name":"jev-preflight","version":"0.1.0","source":{"source":"archive","url":"https://github.com/muse0509/jev-preflight/releases/download/v0.1.0/jev-preflight-plugin-v0.1.0.zip"}}]}`))
	for _, target := range targets {
		writeFixture(t, root, "dist/"+target+"/"+binaryName(target), fixtureBinary(target))
	}
	return root
}

func setMarketplacePin(t *testing.T, root string, pin *string) {
	t.Helper()
	name := ".claude-plugin/marketplace.json"
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	plugin := document["plugins"].([]any)[0].(map[string]any)
	source := plugin["source"].(map[string]any)
	if pin == nil {
		delete(source, "sha256")
	} else {
		source["sha256"] = *pin
	}
	body, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, name, body)
}

func writeFixture(t *testing.T, root, name string, body []byte) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, body, archiveMode(name)); err != nil {
		t.Fatal(err)
	}
}

func fixtureBinary(target string) []byte {
	b := make([]byte, 128)
	arm := strings.HasSuffix(target, "-arm64")
	switch {
	case strings.HasPrefix(target, "darwin-"):
		binary.LittleEndian.PutUint32(b, 0xfeedfacf)
		cpu := uint32(0x1000007)
		if arm {
			cpu = 0x100000c
		}
		binary.LittleEndian.PutUint32(b[4:], cpu)
	case strings.HasPrefix(target, "linux-"):
		copy(b, "\x7fELF")
		b[4], b[5] = 2, 1
		machine := uint16(62)
		if arm {
			machine = 183
		}
		binary.LittleEndian.PutUint16(b[18:], machine)
	case strings.HasPrefix(target, "windows-"):
		copy(b, "MZ")
		binary.LittleEndian.PutUint32(b[60:], 64)
		copy(b[64:], "PE\x00\x00")
		machine := uint16(0x8664)
		if arm {
			machine = 0xaa64
		}
		binary.LittleEndian.PutUint16(b[68:], machine)
	}
	return b
}

func TestArchiveRoundTripAllPlatforms(t *testing.T) {
	root := fixture(t)
	result, err := packageArchive(root, "v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{result.Archive, result.Checksum, result.Unpacked} {
		rel, err := filepath.Rel(filepath.Join(root, "dist"), output)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("output escaped dist: %s", output)
		}
	}
	for _, source := range sourcePaths {
		original, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(source)))
		if err != nil {
			t.Fatal(err)
		}
		unpacked, err := os.ReadFile(filepath.Join(result.Unpacked, filepath.FromSlash(source)))
		if err != nil || !bytes.Equal(original, unpacked) {
			t.Fatalf("required source did not survive extraction: %s", source)
		}
	}
	for _, target := range targets {
		name := "scripts/runtime/" + target + "/" + binaryName(target)
		unpacked, err := os.ReadFile(filepath.Join(result.Unpacked, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(unpacked, fixtureBinary(target)) {
			t.Fatalf("runtime did not survive extraction: %s", target)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(filepath.Join(result.Unpacked, filepath.FromSlash(name)))
			if err != nil || info.Mode().Perm() != 0755 {
				t.Fatalf("runtime lost executable mode: %s", target)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(result.Unpacked, ".claude-plugin", "marketplace.json")); !os.IsNotExist(err) {
		t.Fatal("repository marketplace must not be bundled in the plugin")
	}
	zr, err := zip.OpenReader(result.Archive)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != 12 {
		t.Fatalf("archive contains %d files, want 12", len(zr.File))
	}
	last := ""
	for _, file := range zr.File {
		if file.Name <= last || file.Mode().Perm() != archiveMode(file.Name) || file.Modified.Year() != 1980 {
			t.Fatalf("unstable archive metadata for %s", file.Name)
		}
		last = file.Name
	}
	archive, err := os.ReadFile(result.Archive)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive)
	checksum, err := os.ReadFile(result.Checksum)
	if err != nil || string(checksum) != hex.EncodeToString(sum[:])+"  "+filepath.Base(result.Archive)+"\n" {
		t.Fatal("checksum does not match archive")
	}
	// Repeated packaging replaces only known output files and remains deterministic.
	zr.Close()
	second, err := packageArchive(root, "v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(second.Archive)
	if err != nil || !bytes.Equal(archive, again) {
		t.Fatal("archive bytes changed for identical inputs")
	}
}

func TestArchiveIndependentOfSourceRootMetadataAndLocale(t *testing.T) {
	var firstArchive, firstChecksum []byte
	var pin string
	for i, environment := range []struct {
		timezone, locale string
		mode             os.FileMode
		modified         time.Time
	}{
		{"Pacific/Honolulu", "C", 0600, time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)},
		{"Asia/Tokyo", "ja_JP.UTF-8", 0755, time.Date(2024, 9, 18, 17, 16, 15, 0, time.FixedZone("JST", 9*60*60))},
	} {
		root := unpinnedFixture(t)
		t.Setenv("TZ", environment.timezone)
		t.Setenv("LANG", environment.locale)
		t.Setenv("LC_ALL", environment.locale)
		paths := append([]string(nil), sourcePaths...)
		paths = append(paths, ".claude-plugin/marketplace.json")
		for _, target := range targets {
			paths = append(paths, "dist/"+target+"/"+binaryName(target))
		}
		for _, name := range paths {
			path := filepath.Join(root, filepath.FromSlash(name))
			if err := os.Chmod(path, environment.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, environment.modified, environment.modified); err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			prepared, err := buildArchive(root, "v0.1.0", true)
			if err != nil {
				t.Fatal(err)
			}
			pin = prepared.SHA256
		}
		// Both independent roots must satisfy the first archive's exact pin.
		setMarketplacePin(t, root, &pin)
		result, err := packageArchive(root, "v0.1.0")
		if err != nil {
			t.Fatal(err)
		}
		archive, err := os.ReadFile(result.Archive)
		if err != nil {
			t.Fatal(err)
		}
		checksum, err := os.ReadFile(result.Checksum)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstArchive, firstChecksum = archive, checksum
		} else if !bytes.Equal(archive, firstArchive) || !bytes.Equal(checksum, firstChecksum) || result.SHA256 != pin {
			t.Fatal("source location, metadata, timezone, or locale changed archive bytes")
		}
		reader, err := zip.OpenReader(result.Archive)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range reader.File {
			if file.Method != zip.Deflate || !file.Modified.Equal(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)) || file.Mode().Perm() != archiveMode(file.Name) {
				reader.Close()
				t.Fatal("archive compression, timestamp, or mode depends on source metadata")
			}
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRejectIncompleteOrMismatchedInputs(t *testing.T) {
	for _, kind := range []string{"missing source", "missing runtime", "extra runtime", "wrong architecture", "plugin version", "marketplace version", "marketplace URL"} {
		t.Run(kind, func(t *testing.T) {
			root := fixture(t)
			switch kind {
			case "missing source":
				if err := os.Remove(filepath.Join(root, "LICENSE")); err != nil {
					t.Fatal(err)
				}
			case "missing runtime":
				if err := os.Remove(filepath.Join(root, "dist", "linux-arm64", "jev-preflight")); err != nil {
					t.Fatal(err)
				}
			case "extra runtime":
				writeFixture(t, root, "dist/linux-arm64/unexpected", []byte("unexpected"))
			case "wrong architecture":
				writeFixture(t, root, "dist/linux-arm64/jev-preflight", fixtureBinary("linux-amd64"))
			case "plugin version":
				writeFixture(t, root, ".claude-plugin/plugin.json", []byte(`{"name":"jev-preflight","version":"0.2.0"}`))
			case "marketplace version", "marketplace URL":
				name := ".claude-plugin/marketplace.json"
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
				if err != nil {
					t.Fatal(err)
				}
				if kind == "marketplace version" {
					b = bytes.Replace(b, []byte(`"version":"0.1.0"`), []byte(`"version":"0.2.0"`), 1)
				} else {
					b = bytes.Replace(b, []byte("/download/v0.1.0/"), []byte("/download/v0.2.0/"), 1)
				}
				writeFixture(t, root, name, b)
			}
			if _, err := packageArchive(root, "v0.1.0"); err == nil {
				t.Fatal("unsafe package input accepted")
			}
		})
	}
	for _, version := range []string{"", "0.1.0", "v01.0.0", "v1.0.0-beta", "../outside", "v1.2.3/../../outside"} {
		if _, err := packageArchive(t.TempDir(), version); err == nil {
			t.Fatalf("invalid version accepted: %q", version)
		}
	}
}

func TestRejectSymlinkInputsAndOutput(t *testing.T) {
	for _, name := range []string{"README.md", "dist/linux-arm64/jev-preflight", "dist/jev-preflight-plugin-v0.1.0.zip"} {
		t.Run(name, func(t *testing.T) {
			root := fixture(t)
			outside := filepath.Join(t.TempDir(), "keep")
			if err := os.WriteFile(outside, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, filepath.FromSlash(name))
			if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symlink capability unavailable: %v", err)
			}
			if _, err := packageArchive(root, "v0.1.0"); err == nil {
				t.Fatal("symlink accepted")
			}
			b, err := os.ReadFile(outside)
			if err != nil || string(b) != "preserve" {
				t.Fatal("symlink target changed")
			}
		})
	}
}

func TestUnpackRejectsUnexpectedAndTraversalEntries(t *testing.T) {
	for _, badName := range []string{"../outside", "/absolute", "extra.bin", "scripts\\run", "scripts/run"} {
		t.Run(badName, func(t *testing.T) {
			root := fixture(t)
			result, err := packageArchive(root, "v0.1.0")
			if err != nil {
				t.Fatal(err)
			}
			source, err := zip.OpenReader(result.Archive)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			var buffer bytes.Buffer
			writer := zip.NewWriter(&buffer)
			for _, file := range source.File {
				reader, err := file.Open()
				if err != nil {
					t.Fatal(err)
				}
				w, err := writer.CreateHeader(&file.FileHeader)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.Copy(w, reader); err != nil {
					t.Fatal(err)
				}
				reader.Close()
			}
			header := &zip.FileHeader{Name: badName}
			header.SetMode(0644)
			w, err := writer.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(w, "unexpected"); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			bad := filepath.Join(root, "dist", "bad.zip")
			if err := os.WriteFile(bad, buffer.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := unpackAndVerify(root, "v0.1.0", bad); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func TestPreserveUnexpectedUnpackedFiles(t *testing.T) {
	root := fixture(t)
	name := "dist/unpacked/jev-preflight-plugin-v0.1.0/user-file"
	writeFixture(t, root, name, []byte("preserve"))
	if _, err := packageArchive(root, "v0.1.0"); err == nil {
		t.Fatal("unexpected unpacked file accepted")
	}
	if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name))); err != nil || string(b) != "preserve" {
		t.Fatal("unexpected user file changed")
	}
}

func TestPreparePinAndStrictRepackageAreIdentical(t *testing.T) {
	root := unpinnedFixture(t)
	marketplace := filepath.Join(root, ".claude-plugin", "marketplace.json")
	before, err := os.ReadFile(marketplace)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := buildArchive(root, "v0.1.0", true)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(marketplace)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("pin preparation must never modify marketplace source")
	}
	archive, err := os.ReadFile(prepared.Archive)
	if err != nil {
		t.Fatal(err)
	}
	actual := sha256.Sum256(archive)
	if prepared.SHA256 != hex.EncodeToString(actual[:]) || !sha256Pattern.MatchString(prepared.SHA256) {
		t.Fatal("prepared digest does not describe the generated archive")
	}
	setMarketplacePin(t, root, &prepared.SHA256)
	pinnedSource, err := os.ReadFile(marketplace)
	if err != nil {
		t.Fatal(err)
	}
	strict, err := packageArchive(root, "v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	verified, err := os.ReadFile(strict.Archive)
	if err != nil || !bytes.Equal(archive, verified) || strict.SHA256 != prepared.SHA256 {
		t.Fatal("pin update changed the archive bytes")
	}
	afterStrict, err := os.ReadFile(marketplace)
	if err != nil || !bytes.Equal(pinnedSource, afterStrict) {
		t.Fatal("normal packaging must never modify marketplace source")
	}
}

func TestStrictPackageRejectsMissingInvalidOrMismatchedPin(t *testing.T) {
	for _, tt := range []struct{ name, pin string }{
		{"missing", ""},
		{"empty", ""},
		{"short", strings.Repeat("a", 63)},
		{"long", strings.Repeat("a", 65)},
		{"uppercase", strings.Repeat("A", 64)},
		{"nonhex", strings.Repeat("g", 64)},
		{"mismatch", strings.Repeat("0", 64)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := fixture(t)
			if tt.name == "missing" {
				setMarketplacePin(t, root, nil)
			} else {
				setMarketplacePin(t, root, &tt.pin)
			}
			if _, err := packageArchive(root, "v0.1.0"); err == nil || !strings.Contains(err.Error(), "source.sha256") {
				t.Fatalf("pin was not rejected: %v", err)
			}
		})
	}
}

func TestPinMismatchDoesNotReplacePreviouslyVerifiedArchive(t *testing.T) {
	root := fixture(t)
	initial, err := packageArchive(root, "v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(initial.Archive)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "README.md", []byte("changed after pinning\n"))
	if _, err := packageArchive(root, "v0.1.0"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("changed artifact was not rejected: %v", err)
	}
	after, err := os.ReadFile(initial.Archive)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed verification replaced the existing archive")
	}
}
