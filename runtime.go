package detectharness

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type runtimeEnvironment struct {
	platform string
	homeDir  string
	env      map[string]string
}

func hostRuntime() runtimeEnvironment {
	env := make(map[string]string)
	for _, item := range os.Environ() {
		key, value, found := strings.Cut(item, "=")
		if found {
			env[key] = value
		}
	}
	home, _ := os.UserHomeDir()
	return runtimeEnvironment{platform: runtime.GOOS, homeDir: home, env: env}
}

// WithEnvironment overrides platform, home, and environment resolution.
func WithEnvironment(options DetectOptions) Option {
	return func(config *installerOptions) error {
		if options.Platform != "" {
			config.runtime.platform = options.Platform
		}
		if options.HomeDir != "" {
			config.runtime.homeDir = options.HomeDir
		}
		if options.Env != nil {
			config.runtime.env = make(map[string]string, len(options.Env))
			for key, value := range options.Env {
				config.runtime.env[key] = value
			}
		}
		if config.runtime.homeDir == "" {
			return errors.New("home directory is unavailable")
		}
		return nil
	}
}

func withSystem(system hostSystem) Option {
	return func(config *installerOptions) error {
		if system == nil {
			return errors.New("system cannot be nil")
		}
		config.system = system
		return nil
	}
}

func (r runtimeEnvironment) home(parts ...string) string {
	all := append([]string{r.homeDir}, parts...)
	return filepath.Join(all...)
}

func (r runtimeEnvironment) appSupport(parts ...string) string {
	all := append([]string{"Library", "Application Support"}, parts...)
	return r.home(all...)
}

type pathResolution struct {
	path   string
	reason string
}

func (r runtimeEnvironment) appData() pathResolution {
	if r.platform != "windows" {
		return pathResolution{reason: "APPDATA is only defined for Windows clients"}
	}
	value, _ := r.lookupEnv("APPDATA")
	if value == "" || !windowsAbsolute(value) {
		return pathResolution{reason: "APPDATA must be an absolute path"}
	}
	return pathResolution{path: value}
}

func (r runtimeEnvironment) localAppData() pathResolution {
	if r.platform != "windows" {
		return pathResolution{reason: "LOCALAPPDATA is only defined for Windows clients"}
	}
	value, _ := r.lookupEnv("LOCALAPPDATA")
	if value == "" || !windowsAbsolute(value) {
		return pathResolution{reason: "LOCALAPPDATA must be an absolute path"}
	}
	return pathResolution{path: value}
}

func windowsAbsolute(value string) bool {
	if len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		return true
	}
	trimmed := strings.TrimPrefix(strings.ReplaceAll(value, "/", "\\"), "\\\\")
	parts := strings.Split(trimmed, "\\")
	return strings.HasPrefix(value, "\\\\") && len(parts) >= 2 && parts[0] != "" && parts[1] != ""
}

func (r runtimeEnvironment) xdgConfig() pathResolution {
	if configured, exists := r.lookupEnv("XDG_CONFIG_HOME"); exists {
		if !filepath.IsAbs(configured) {
			return pathResolution{reason: "XDG_CONFIG_HOME must be an absolute path"}
		}
		return pathResolution{path: configured}
	}
	return pathResolution{path: r.home(".config")}
}

func (r runtimeEnvironment) lookupEnv(name string) (string, bool) {
	if value, found := r.env[name]; found {
		return value, true
	}
	if r.platform == "windows" {
		for key, value := range r.env {
			if strings.EqualFold(key, name) {
				return value, true
			}
		}
	}
	return "", false
}

type osSystem struct{}

type hostSystem interface {
	Lstat(string) (fs.FileInfo, error)
	Stat(string) (fs.FileInfo, error)
	ReadDir(string) ([]fs.DirEntry, error)
	ReadFile(string) ([]byte, error)
	WriteFileAtomic(path string, expected fileSnapshot, content []byte) error
}

func (osSystem) Lstat(path string) (fs.FileInfo, error)     { return os.Lstat(path) }
func (osSystem) Stat(path string) (fs.FileInfo, error)      { return os.Stat(path) }
func (osSystem) ReadDir(path string) ([]fs.DirEntry, error) { return os.ReadDir(path) }
func (osSystem) ReadFile(path string) ([]byte, error)       { return os.ReadFile(path) }

func (osSystem) WriteFileAtomic(path string, expected fileSnapshot, content []byte) error {
	current, err := readSnapshot(osSystem{}, path)
	if err != nil {
		return err
	}
	if current.exists != expected.exists || !equalBytes(current.raw, expected.raw) {
		return errors.New("configuration changed after it was planned")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	lockPath := filepath.Join(directory, "."+filepath.Base(path)+".detect-harness.lock")
	releaseLock, err := acquireConfigLock(lockPath)
	if err != nil {
		return fmt.Errorf("acquire config lock: %w", err)
	}
	defer releaseLock()
	current, err = readSnapshot(osSystem{}, path)
	if err != nil {
		return err
	}
	if current.exists != expected.exists || !equalBytes(current.raw, expected.raw) {
		return errors.New("configuration changed after it was planned")
	}
	temporary, err := os.CreateTemp(directory, ".detect-harness-*")
	if err != nil {
		return fmt.Errorf("create staged config: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	mode := fs.FileMode(0o600)
	if expected.exists {
		mode = expected.mode.Perm()
	}
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set staged config permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write staged config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync staged config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close staged config: %w", err)
	}
	closed = true
	current, err = readSnapshot(osSystem{}, path)
	if err != nil {
		return err
	}
	if current.exists != expected.exists || !equalBytes(current.raw, expected.raw) {
		return errors.New("configuration changed while the replacement was staged")
	}
	if err := replaceFile(temporaryPath, path, expected.exists); err != nil {
		return fmt.Errorf("publish staged config: %w", err)
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

func acquireConfigLock(path string) (func(), error) {
	const staleAfter = 5 * time.Minute
	for attempt := 0; attempt < 2; attempt++ {
		lock, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, writeErr := fmt.Fprintln(lock, os.Getpid()); writeErr != nil {
				_ = lock.Close()
				_ = os.Remove(path)
				return nil, writeErr
			}
			if syncErr := lock.Sync(); syncErr != nil {
				_ = lock.Close()
				_ = os.Remove(path)
				return nil, syncErr
			}
			if closeErr := lock.Close(); closeErr != nil {
				_ = os.Remove(path)
				return nil, closeErr
			}
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, statErr
		}
		if time.Since(info.ModTime()) <= staleAfter {
			return nil, errors.New("another detect-harness operation holds the config lock")
		}
		if removeErr := os.Remove(path); removeErr != nil {
			return nil, fmt.Errorf("remove stale config lock: %w", removeErr)
		}
	}
	return nil, errors.New("could not acquire config lock")
}
