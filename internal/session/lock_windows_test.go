package session

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestWindowsLockReleaseWithOpenHandle(t *testing.T) {
	for _, kind := range []string{"session", "notice"} {
		t.Run(kind, func(t *testing.T) {
			store := mustOpen(t, testOptions(t, false))
			path := filepath.Join(store.base, ".locks", store.owner.PromptHash)
			acquire := store.Lock
			if kind == "notice" {
				path = filepath.Join(store.base, ".notices", store.owner.SessionHash+".lock")
				acquire = func() (func(), bool, error) { return acquireDirectoryLock(path) }
			}
			unlock, acquired, err := acquire()
			if err != nil || !acquired {
				t.Fatalf("initial lock: %v", err)
			}
			name, err := syscall.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := syscall.CreateFile(name, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
			if err != nil {
				t.Fatalf("open shared directory handle: %v", err)
			}
			closed := false
			defer func() {
				if !closed {
					syscall.CloseHandle(handle)
				}
			}()
			unlock()
			// The old handle still exists, so removing the canonical directory
			// directly would leave that name unavailable until the handle closes.
			unlockNext, acquired, err := acquire()
			if err != nil || !acquired {
				t.Fatalf("reacquire while old handle is open: %v", err)
			}
			defer unlockNext()
			unlock()
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("old cleanup removed replacement lock: %v", err)
			}
			if _, acquired, err := acquire(); err != nil || acquired {
				t.Fatal("replacement lock does not exclude another owner")
			}
			unlockNext()
			if kind == "notice" {
				if yes, err := store.Notice("api"); err != nil || !yes {
					t.Fatalf("notice after lock release: %v", err)
				}
				if yes, err := store.Notice("api"); err != nil || yes {
					t.Fatalf("notice emitted twice: %v", err)
				}
			}
			if err := syscall.CloseHandle(handle); err != nil {
				t.Fatal(err)
			}
			closed = true
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".released-") {
					t.Fatal("retired lock remains after its last handle closes")
				}
			}
		})
	}
}
