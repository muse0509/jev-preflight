// Command package assembles and checks a local plugin release archive.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxFileBytes = 64 << 20

var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var sourcePaths = []string{
	".claude-plugin/plugin.json",
	"hooks/hooks.json", "policies/default.json", "scripts/run", "README.md", "LICENSE",
}

var targets = []string{
	"darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64", "windows-amd64", "windows-arm64",
}

type artifact struct {
	Archive, Checksum, Unpacked string
	SHA256                      string
}

type entry struct {
	name string
	body []byte
	mode os.FileMode
}

func main() {
	root := flag.String("root", ".", "source repository root")
	version := flag.String("version", "", "release version (vX.Y.Z)")
	preparePin := flag.Bool("prepare-pin", false, "prepare an archive digest without checking the existing marketplace pin; never edits source")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "package: unexpected arguments")
		os.Exit(2)
	}
	var result artifact
	var err error
	if *preparePin {
		result, err = buildArchive(*root, *version, true)
	} else {
		result, err = packageArchive(*root, *version)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "package:", err)
		os.Exit(1)
	}
	fmt.Println("Archive:", result.Archive)
	fmt.Println("Checksum:", result.Checksum)
	fmt.Println("Verified plugin:", result.Unpacked)
	fmt.Println("SHA256:", result.SHA256)
	if *preparePin {
		fmt.Println("Pin preparation only: update marketplace source.sha256, then run package without -prepare-pin.")
	}
}

func packageArchive(root, version string) (artifact, error) {
	return buildArchive(root, version, false)
}

func buildArchive(root, version string, preparePin bool) (artifact, error) {
	if !versionPattern.MatchString(version) {
		return artifact{}, errors.New("version must be vX.Y.Z")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return artifact{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return artifact{}, err
	}
	var entries []entry
	for _, name := range sourcePaths {
		body, err := readRegular(root, name)
		if err != nil {
			return artifact{}, fmt.Errorf("required source %s: %w", name, err)
		}
		entries = append(entries, entry{name: name, body: body, mode: archiveMode(name)})
	}
	marketplace, err := readRegular(root, ".claude-plugin/marketplace.json")
	if err != nil {
		return artifact{}, err
	}
	metadata := append(append([]entry(nil), entries...), entry{name: ".claude-plugin/marketplace.json", body: marketplace})
	if err := validateVersions(metadata, version, true); err != nil {
		return artifact{}, err
	}
	pin, err := marketplacePin(marketplace)
	if err != nil {
		return artifact{}, err
	}
	if !preparePin && !sha256Pattern.MatchString(pin) {
		return artifact{}, errors.New("marketplace source.sha256 must contain 64 lowercase hexadecimal characters")
	}
	for _, target := range targets {
		name := binaryName(target)
		dir := "dist/" + target
		if _, err := checkedDirectory(root, dir, false); err != nil {
			return artifact{}, err
		}
		files, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil || len(files) != 1 || files[0].Name() != name {
			return artifact{}, fmt.Errorf("expected only %s in %s", name, dir)
		}
		body, err := readRegular(root, dir+"/"+name)
		if err != nil {
			return artifact{}, err
		}
		if !matchesTarget(body, target) {
			return artifact{}, fmt.Errorf("binary format or architecture does not match %s", target)
		}
		archivePath := "scripts/runtime/" + target + "/" + name
		entries = append(entries, entry{name: archivePath, body: body, mode: archiveMode(archivePath)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	dist, err := checkedDirectory(root, "dist", true)
	if err != nil {
		return artifact{}, err
	}
	archive := filepath.Join(dist, "jev-preflight-plugin-"+version+".zip")
	tmp, err := os.CreateTemp(dist, ".package-*.zip")
	if err != nil {
		return artifact{}, err
	}
	defer os.Remove(tmp.Name())
	zw := zip.NewWriter(tmp)
	for _, item := range entries {
		header := &zip.FileHeader{Name: item.name, Method: zip.Deflate, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}
		header.SetMode(item.mode)
		w, createErr := zw.CreateHeader(header)
		if createErr == nil {
			_, createErr = w.Write(item.body)
		}
		if createErr != nil {
			_ = zw.Close()
			_ = tmp.Close()
			return artifact{}, createErr
		}
	}
	if err := zw.Close(); err != nil {
		_ = tmp.Close()
		return artifact{}, err
	}
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return artifact{}, err
	}
	if err := tmp.Close(); err != nil {
		return artifact{}, err
	}
	digest, err := fileSHA256(tmp.Name())
	if err != nil {
		return artifact{}, err
	}
	if !preparePin && digest != pin {
		return artifact{}, errors.New("marketplace source.sha256 does not match the generated archive")
	}
	if err := replaceFile(tmp.Name(), archive); err != nil {
		return artifact{}, err
	}
	unpacked, err := unpackAndVerify(root, version, archive)
	if err != nil {
		return artifact{}, err
	}
	for _, item := range entries {
		body, err := readRegular(unpacked, item.name)
		if err != nil || !bytes.Equal(body, item.body) {
			return artifact{}, fmt.Errorf("unpacked bytes differ: %s", item.name)
		}
	}
	checksum := archive + ".sha256"
	text := digest + "  " + filepath.Base(archive) + "\n"
	if err := atomicWrite(checksum, []byte(text), 0644); err != nil {
		return artifact{}, err
	}
	return artifact{Archive: archive, Checksum: checksum, Unpacked: unpacked, SHA256: digest}, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, f)
	closeErr := f.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func marketplacePin(body []byte) (string, error) {
	var marketplace struct {
		Plugins []struct {
			Name   string
			Source map[string]json.RawMessage
		}
	}
	if json.Unmarshal(body, &marketplace) != nil {
		return "", errors.New("invalid marketplace SHA-256 field")
	}
	for _, plugin := range marketplace.Plugins {
		if plugin.Name == "jev-preflight" {
			var pin string
			if field, ok := plugin.Source["sha256"]; ok && json.Unmarshal(field, &pin) != nil {
				return "", errors.New("invalid marketplace SHA-256 field")
			}
			return pin, nil
		}
	}
	return "", errors.New("missing marketplace plugin")
}

func unpackAndVerify(root, version, archive string) (string, error) {
	if !versionPattern.MatchString(version) {
		return "", errors.New("invalid archive version")
	}
	expected := expectedPaths()
	destination, err := checkedDirectory(root, "dist/unpacked/jev-preflight-plugin-"+version, true)
	if err != nil {
		return "", err
	}
	// Do not overwrite an unexpected user file or follow a pre-existing symlink.
	if err := validateExisting(destination, expected); err != nil {
		return "", err
	}
	r, err := zip.OpenReader(archive)
	if err != nil {
		return "", err
	}
	defer r.Close()
	seen := make(map[string]bool, len(expected))
	var entries []entry
	for _, file := range r.File {
		mode, exists := expected[file.Name]
		if !exists || seen[file.Name] || file.Mode() != mode || file.UncompressedSize64 > maxFileBytes {
			return "", errors.New("archive contains an unexpected, duplicate, or unsafe entry")
		}
		seen[file.Name] = true
		reader, err := file.Open()
		if err != nil {
			return "", err
		}
		body, readErr := io.ReadAll(io.LimitReader(reader, maxFileBytes+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || len(body) > maxFileBytes {
			return "", errors.New("cannot read bounded archive entry")
		}
		entries = append(entries, entry{name: file.Name, body: body, mode: mode})
	}
	if len(seen) != len(expected) {
		return "", errors.New("archive is missing required plugin files")
	}
	if err := validateVersions(entries, version, false); err != nil {
		return "", err
	}
	for _, item := range entries {
		if strings.HasPrefix(item.name, "scripts/runtime/") {
			target := strings.Split(item.name, "/")[2]
			if !matchesTarget(item.body, target) {
				return "", errors.New("archive binary does not match its target")
			}
		}
		if _, err := checkedDirectory(destination, filepath.ToSlash(filepath.Dir(item.name)), true); err != nil {
			return "", err
		}
		if err := atomicWrite(filepath.Join(destination, filepath.FromSlash(item.name)), item.body, item.mode); err != nil {
			return "", err
		}
	}
	return destination, nil
}

func expectedPaths() map[string]os.FileMode {
	paths := make(map[string]os.FileMode)
	for _, name := range sourcePaths {
		paths[name] = archiveMode(name)
	}
	for _, target := range targets {
		name := "scripts/runtime/" + target + "/" + binaryName(target)
		paths[name] = archiveMode(name)
	}
	return paths
}

func validateVersions(entries []entry, version string, requireMarketplace bool) error {
	want := strings.TrimPrefix(version, "v")
	foundPlugin, foundMarketplace := false, false
	for _, item := range entries {
		switch item.name {
		case ".claude-plugin/plugin.json":
			var manifest struct{ Name, Version string }
			if json.Unmarshal(item.body, &manifest) != nil || manifest.Name != "jev-preflight" || manifest.Version != want {
				return errors.New("plugin manifest version must match the release version")
			}
			foundPlugin = true
		case ".claude-plugin/marketplace.json":
			var marketplace struct {
				Metadata struct{ Version string }
				Plugins  []struct {
					Name, Version string
					Source        struct{ Source, URL string }
				}
			}
			if json.Unmarshal(item.body, &marketplace) != nil || (marketplace.Metadata.Version != "" && marketplace.Metadata.Version != want) {
				return errors.New("invalid marketplace version")
			}
			count := 0
			for _, plugin := range marketplace.Plugins {
				if plugin.Name == "jev-preflight" {
					count++
					if plugin.Version != want {
						return errors.New("marketplace plugin version must match the release version")
					}
					url := "https://github.com/muse0509/jev-preflight/releases/download/" + version + "/jev-preflight-plugin-" + version + ".zip"
					if plugin.Source.Source != "archive" || plugin.Source.URL != url {
						return errors.New("marketplace archive URL must match the release version")
					}
				}
			}
			if count != 1 {
				return errors.New("marketplace must contain exactly one jev-preflight entry")
			}
			foundMarketplace = true
		}
	}
	if !foundPlugin || (requireMarketplace && !foundMarketplace) {
		return errors.New("missing plugin metadata")
	}
	return nil
}

func binaryName(target string) string {
	if strings.HasPrefix(target, "windows-") {
		return "jev-preflight.exe"
	}
	return "jev-preflight"
}

func archiveMode(name string) os.FileMode {
	if name == "scripts/run" || strings.HasPrefix(name, "scripts/runtime/") {
		return 0755
	}
	return 0644
}

func matchesTarget(body []byte, target string) bool {
	arm := strings.HasSuffix(target, "-arm64")
	switch {
	case strings.HasPrefix(target, "darwin-"):
		cpu := uint32(0x1000007)
		if arm {
			cpu = 0x100000c
		}
		return len(body) >= 32 && binary.LittleEndian.Uint32(body[:4]) == 0xfeedfacf && binary.LittleEndian.Uint32(body[4:8]) == cpu
	case strings.HasPrefix(target, "linux-"):
		machine := uint16(62)
		if arm {
			machine = 183
		}
		return len(body) >= 64 && string(body[:4]) == "\x7fELF" && body[4] == 2 && body[5] == 1 && binary.LittleEndian.Uint16(body[18:20]) == machine
	case strings.HasPrefix(target, "windows-"):
		if len(body) < 64 || string(body[:2]) != "MZ" {
			return false
		}
		offset := uint64(binary.LittleEndian.Uint32(body[60:64]))
		machine := uint16(0x8664)
		if arm {
			machine = 0xaa64
		}
		return offset+6 <= uint64(len(body)) && string(body[offset:offset+4]) == "PE\x00\x00" && binary.LittleEndian.Uint16(body[offset+4:offset+6]) == machine
	}
	return false
}

func checkedDirectory(root, relative string, create bool) (string, error) {
	current := root
	if relative == "." {
		return root, nil
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\:") {
			return "", errors.New("unsafe package path")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && create {
			if err = os.Mkdir(current, 0755); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("package paths must be real directories")
		}
	}
	return current, nil
}

func readRegular(root, name string) ([]byte, error) {
	if _, err := checkedDirectory(root, filepath.ToSlash(filepath.Dir(name)), false); err != nil {
		return nil, err
	}
	file := filepath.Join(root, filepath.FromSlash(name))
	info, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return nil, errors.New("package input must be a bounded regular file")
	}
	return os.ReadFile(file)
}

func validateExisting(root string, expected map[string]os.FileMode) error {
	allowedDirs := map[string]bool{".": true}
	for name := range expected {
		for dir := filepath.ToSlash(filepath.Dir(name)); dir != "."; dir = filepath.ToSlash(filepath.Dir(dir)) {
			allowedDirs[dir] = true
		}
	}
	return filepath.WalkDir(root, func(path string, item os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if item.IsDir() && allowedDirs[relative] {
			return nil
		}
		if _, ok := expected[relative]; ok && item.Type().IsRegular() {
			return nil
		}
		return errors.New("unpacked destination contains an unexpected file or symlink")
	})
}

func atomicWrite(path string, body []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".package-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceFile(tmp.Name(), path)
}

func replaceFile(source, destination string) error {
	if info, err := os.Lstat(destination); err == nil && !info.Mode().IsRegular() {
		return errors.New("refusing to replace a non-regular package file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, destination)
}
