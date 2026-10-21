package server

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestJWTAuthDirectoryPersistsAcrossRuntimeReplacement(t *testing.T) {
	base, data := t.TempDir(), t.TempDir()
	auth := filepath.Join(t.TempDir(), "auth")
	t.Setenv("ARTEX_AUTH_DIR", auth)
	first, err := loadOrCreateJWTKey(base, data)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signJWT(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateJWTKey(t.TempDir(), data)
	if err != nil || !bytes.Equal(first, second) || !verifyJWT(token, second) {
		t.Fatalf("persisted JWT key changed: %v", err)
	}
	for _, dir := range []string{base, data} {
		if _, err := os.Stat(filepath.Join(dir, jwtKeyFilename)); !os.IsNotExist(err) {
			t.Fatal("JWT key appeared outside the private auth directory")
		}
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{auth: 0700, filepath.Join(auth, jwtKeyFilename): 0600} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("private mode for %s: %v", path, err)
			}
		}
	}
}

func TestJWTAuthDirectoryMigratesExistingKey(t *testing.T) {
	for _, legacyInData := range []bool{false, true} {
		t.Run(map[bool]string{false: "base", true: "workspace"}[legacyInData], func(t *testing.T) {
			base, data := t.TempDir(), t.TempDir()
			t.Setenv("ARTEX_AUTH_DIR", filepath.Join(t.TempDir(), "auth"))
			legacy := filepath.Join(base, jwtKeyFilename)
			if legacyInData {
				legacy = filepath.Join(data, jwtKeyFilename)
			}
			key := []byte(strings.Repeat("legacy", 8))
			if err := os.WriteFile(legacy, key, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := loadOrCreateJWTKey(base, data)
			if err != nil || !bytes.Equal(got, key) {
				t.Fatalf("legacy key was replaced: %v", err)
			}
			if _, err := os.Stat(legacy); !os.IsNotExist(err) {
				t.Fatal("legacy key was left behind")
			}
		})
	}
}

func TestJWTExistingInvalidOrUnreadableKeyIsNotReplaced(t *testing.T) {
	for _, kind := range []string{"short", "directory", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			auth, base, data := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("ARTEX_AUTH_DIR", auth)
			path := filepath.Join(auth, jwtKeyFilename)
			original := []byte("short")
			if kind == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if kind == "unreadable" {
					if runtime.GOOS == "windows" || os.Geteuid() == 0 {
						t.Skip("file permissions do not restrict this test user")
					}
					original = []byte(strings.Repeat("k", 32))
				}
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "unreadable" {
					if err := os.Chmod(path, 0); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := loadOrCreateJWTKey(base, data); err == nil {
				t.Fatal("invalid existing key accepted or silently replaced")
			}
			if kind != "directory" {
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, original) {
					t.Fatal("invalid original key changed")
				}
			}
		})
	}
}

func TestJWTAuthDirectoryRejectsBrowsableWorkspace(t *testing.T) {
	base, data := t.TempDir(), t.TempDir()
	for _, dir := range []string{data, filepath.Join(data, "auth")} {
		t.Setenv("ARTEX_AUTH_DIR", dir)
		if _, err := loadOrCreateJWTKey(base, data); err == nil {
			t.Fatal("JWT key stored inside browsable data")
		}
	}
}

func TestJWTFailedLegacyRemovalDoesNotLeaveCommittedMigration(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not restrict this test user")
	}
	auth, base, data := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("ARTEX_AUTH_DIR", auth)
	legacy := filepath.Join(data, jwtKeyFilename)
	original := []byte(strings.Repeat("legacy", 8))
	if err := os.WriteFile(legacy, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(data, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(data, 0700)
	for i := 0; i < 2; i++ {
		if _, err := loadOrCreateJWTKey(base, data); err == nil {
			t.Fatal("migration with an undeletable legacy key was accepted")
		}
		if _, err := os.Stat(filepath.Join(auth, jwtKeyFilename)); !os.IsNotExist(err) {
			t.Fatal("failed migration left a destination that bypasses cleanup on restart")
		}
	}
	got, err := os.ReadFile(legacy)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("failed migration changed the original key")
	}
}

func TestJWTDefaultKeyLocationUnchanged(t *testing.T) {
	t.Setenv("ARTEX_AUTH_DIR", "")
	base, data := t.TempDir(), t.TempDir()
	first, err := loadOrCreateJWTKey(base, data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(base, jwtKeyFilename))
	if err != nil || !bytes.Equal(got, first) {
		t.Fatalf("default key location changed: %v", err)
	}
	second, err := loadOrCreateJWTKey(base, data)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("default key did not persist: %v", err)
	}
}

func TestJWTActiveDuplicateRemovedFromWorkspace(t *testing.T) {
	for _, alreadyPrivate := range []bool{false, true} {
		t.Run(map[bool]string{false: "base_and_data", true: "auth_and_data"}[alreadyPrivate], func(t *testing.T) {
			auth, base, data := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("ARTEX_AUTH_DIR", auth)
			original := []byte(strings.Repeat("active", 8))
			source := base
			if alreadyPrivate {
				source = auth
			}
			for _, dir := range []string{source, data} {
				if err := os.WriteFile(filepath.Join(dir, jwtKeyFilename), original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := loadOrCreateJWTKey(base, data)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("active key changed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(data, jwtKeyFilename)); !os.IsNotExist(err) {
				t.Fatal("active key copy remains in browsable data")
			}
		})
	}
}

func TestJWTActiveDuplicateCleanupMustSucceedAcrossRestarts(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not restrict this test user")
	}
	for _, alreadyPrivate := range []bool{false, true} {
		t.Run(map[bool]string{false: "base_and_data", true: "auth_and_data"}[alreadyPrivate], func(t *testing.T) {
			auth, base, data := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("ARTEX_AUTH_DIR", auth)
			original := []byte(strings.Repeat("active", 8))
			source := base
			if alreadyPrivate {
				source = auth
			}
			for _, dir := range []string{source, data} {
				if err := os.WriteFile(filepath.Join(dir, jwtKeyFilename), original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(data, 0500); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(data, 0700)
			for i := 0; i < 2; i++ {
				if _, err := loadOrCreateJWTKey(base, data); err == nil {
					t.Fatal("restart bypassed failed active-key cleanup")
				}
			}
			if err := os.Chmod(data, 0700); err != nil {
				t.Fatal(err)
			}
			got, err := loadOrCreateJWTKey(base, data)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("cleanup retry changed the active key: %v", err)
			}
			if _, err := os.Stat(filepath.Join(data, jwtKeyFilename)); !os.IsNotExist(err) {
				t.Fatal("cleanup retry left the exposed copy")
			}
		})
	}
}

func TestJWTDifferentLegacyKeyIsNotRemoved(t *testing.T) {
	auth, base, data := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("ARTEX_AUTH_DIR", auth)
	active, other := []byte(strings.Repeat("active", 8)), []byte(strings.Repeat("other", 8))
	if err := os.WriteFile(filepath.Join(auth, jwtKeyFilename), active, 0600); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(data, jwtKeyFilename)
	if err := os.WriteFile(legacy, other, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := loadOrCreateJWTKey(base, data); err != nil || !bytes.Equal(got, active) {
		t.Fatalf("private active key changed: %v", err)
	}
	if got, err := os.ReadFile(legacy); err != nil || !bytes.Equal(got, other) {
		t.Fatal("unrelated legacy key was removed or changed")
	}
}
