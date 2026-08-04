package detectharness

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ID is the stable identifier of a supported AI harness.
type ID string

const (
	ClaudeDesktop ID = "claude-desktop"
	ClaudeCode    ID = "claude-code"
	Cursor        ID = "cursor"
	Codex         ID = "codex"
	GeminiCLI     ID = "gemini-cli"
	Windsurf      ID = "windsurf"
	Zed           ID = "zed"
	Cline         ID = "cline"
	RooCode       ID = "roo-code" // Deprecated: canonical id is ZooCode; kept as an alias for backward compatibility.
	ZooCode       ID = "zoo-code"
	AmazonQ       ID = "amazon-q"
	Continue      ID = "continue"
	OpenCode      ID = "opencode"
	VSCode        ID = "vscode"
)

// StdioServer is the single, client-independent definition of a local MCP server.
type StdioServer struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

var serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (s StdioServer) validate() error {
	if !serverNamePattern.MatchString(s.Name) {
		return errors.New("server name must be 1-64 ASCII letters, digits, dots, underscores, or hyphens")
	}
	if s.Name == "__proto__" || s.Name == "constructor" || s.Name == "toString" {
		return errors.New("server name conflicts with an object prototype property")
	}
	if strings.TrimSpace(s.Command) == "" || strings.ContainsRune(s.Command, 0) {
		return errors.New("server command must be non-empty and cannot contain NUL")
	}
	for _, arg := range s.Args {
		if strings.ContainsRune(arg, 0) {
			return errors.New("server arguments cannot contain NUL")
		}
	}
	for key, value := range s.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return fmt.Errorf("invalid environment variable %q", key)
		}
	}
	return nil
}

// Harness describes a supported AI harness without probing the local machine.
type Harness struct {
	ID         ID     `json:"id"`
	Name       string `json:"name"`
	ReloadHint string `json:"reloadHint"`
	// Project describes project-scoped (directory-local) configuration support.
	// It is nil for harnesses that only support a global configuration.
	Project *ProjectScope `json:"project,omitempty"`
}

// ProjectScope describes how a harness supports directory-local MCP
// configuration. It is informational metadata for library consumers building
// install UX; detect-harness never creates files unless asked to plan/apply.
type ProjectScope struct {
	// Path is the canonical project file location relative to the project
	// directory (for example ".mcp.json").
	Path string `json:"path"`
	// ReloadHint describes how a user picks up project-scope changes.
	ReloadHint string `json:"reloadHint"`
	// Lifecycle summarizes creation, merge, and preemptive-create behavior.
	Lifecycle string `json:"lifecycle"`
	// Shareable indicates the file is intended to be committed to version control.
	Shareable bool `json:"shareable"`
	// TrustGate indicates the harness gates project servers behind a trust or
	// approval dialog before they are loaded.
	TrustGate bool `json:"trustGate"`
}

// ScopeMode selects the configuration scope.
type ScopeMode string

const (
	// ScopeGlobal is the zero-value scope: system/user configuration used today.
	// It matches existing global behavior exactly.
	ScopeGlobal ScopeMode = ""
	// ScopeProject targets a directory-local (per-project) configuration.
	ScopeProject ScopeMode = "project"
)

// Scope selects where configuration is detected and applied. The zero value is
// global scope and preserves existing behavior.
type Scope struct {
	Mode ScopeMode `json:"mode,omitempty"`
	// Dir is the project directory. It is required when Mode is ScopeProject and
	// ignored otherwise. Relative paths are resolved against the process work
	// directory. Absolute paths are recommended.
	Dir string `json:"dir,omitempty"`
}

// ProjectScope returns a scope that targets directory-local configuration in dir.
func ProjectScopeDir(dir string) Scope { return Scope{Mode: ScopeProject, Dir: dir} }

// normalize validates the scope and resolves the project directory to an
// absolute path. The global zero-value scope is always valid.
func (s Scope) normalize() (Scope, error) {
	switch s.Mode {
	case ScopeGlobal:
		return Scope{}, nil
	case ScopeProject:
		if strings.TrimSpace(s.Dir) == "" {
			return Scope{}, errors.New("project scope requires a directory")
		}
		absolute, err := filepath.Abs(s.Dir)
		if err != nil {
			return Scope{}, fmt.Errorf("resolve project directory: %w", err)
		}
		return Scope{Mode: ScopeProject, Dir: absolute}, nil
	default:
		return Scope{}, fmt.Errorf("unsupported scope mode %q", s.Mode)
	}
}

// DetectionState distinguishes absence from an environment that could not be inspected.
type DetectionState string

const (
	Detected    DetectionState = "present"
	NotDetected DetectionState = "absent"
	Unavailable DetectionState = "unavailable"
)

// Detection is the result of probing one harness.
type Detection struct {
	Harness
	State       DetectionState `json:"state"`
	Evidence    []string       `json:"evidence,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	ConfigPath  string         `json:"configPath,omitempty"`
	ConfigError string         `json:"configError,omitempty"`
	Scope       ScopeMode      `json:"scope,omitempty"`
	ScopeDir    string         `json:"scopeDir,omitempty"`
}

// DetectOptions overrides the host environment. It is primarily useful for tests and sandboxes.
type DetectOptions struct {
	Platform string
	HomeDir  string
	Env      map[string]string
	// Scope selects global (zero value) or project-scoped detection.
	Scope Scope
}

// DesiredState describes the registration state a plan should establish.
type DesiredState string

const (
	Present DesiredState = "present"
	Absent  DesiredState = "absent"
)

// ConflictPolicy controls same-name entries that do not exactly match Server.
type ConflictPolicy string

const (
	ConflictError   ConflictPolicy = "error"
	ConflictReplace ConflictPolicy = "replace"
)

// PlanOptions controls planning without performing writes.
type PlanOptions struct {
	ConflictPolicy ConflictPolicy
	// Scope selects global (zero value) or project-scoped planning.
	Scope Scope
}

// ChangeState is the outcome of planning one harness.
type ChangeState string

const (
	ChangeReady       ChangeState = "ready"
	ChangeNoop        ChangeState = "noop"
	ChangeConflict    ChangeState = "conflict"
	ChangeUnavailable ChangeState = "unavailable"
)

// Change is a serializable description of one planned harness change.
type Change struct {
	HarnessID ID           `json:"harnessId"`
	Name      string       `json:"name"`
	Path      string       `json:"path,omitempty"`
	Desired   DesiredState `json:"desired"`
	State     ChangeState  `json:"state"`
	Action    string       `json:"action,omitempty"`
	Reason    string       `json:"reason,omitempty"`
	Scope     ScopeMode    `json:"scope,omitempty"`
	ScopeDir  string       `json:"scopeDir,omitempty"`
	Before    string       `json:"-"`
	After     string       `json:"-"`
}

// Plan is an immutable set of configuration changes. Its internal snapshots are
// retained so Apply can reject files changed after planning.
type Plan struct {
	changes []plannedChange
}

// Changes returns a copy suitable for display or serialization.
func (p *Plan) Changes() []Change {
	if p == nil {
		return nil
	}
	out := make([]Change, len(p.changes))
	for i := range p.changes {
		out[i] = p.changes[i].Change
	}
	return out
}

// ApplyState is the outcome of applying one planned change.
type ApplyState string

const (
	Applied       ApplyState = "applied"
	ApplyNoop     ApplyState = "noop"
	ApplySkipped  ApplyState = "skipped"
	ApplyConflict ApplyState = "conflict"
	ApplyFailed   ApplyState = "failed"
)

// Result reports the outcome for one harness without hiding partial success.
type Result struct {
	HarnessID ID           `json:"harnessId"`
	Name      string       `json:"name"`
	Path      string       `json:"path,omitempty"`
	Desired   DesiredState `json:"desired"`
	State     ApplyState   `json:"state"`
	Action    string       `json:"action,omitempty"`
	Reason    string       `json:"reason,omitempty"`
	Scope     ScopeMode    `json:"scope,omitempty"`
	ScopeDir  string       `json:"scopeDir,omitempty"`
}

// Option customizes an Installer.
type Option func(*installerOptions) error

type installerOptions struct {
	runtime runtimeEnvironment
	system  hostSystem
}

// Installer detects harnesses and manages one MCP server registration.
type Installer struct {
	server  StdioServer
	runtime runtimeEnvironment
	system  hostSystem
}

// New validates server once and returns a reusable installer.
func New(server StdioServer, options ...Option) (*Installer, error) {
	if err := server.validate(); err != nil {
		return nil, err
	}
	configured := installerOptions{runtime: hostRuntime(), system: osSystem{}}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(&configured); err != nil {
			return nil, err
		}
	}
	if configured.system == nil {
		return nil, errors.New("system cannot be nil")
	}
	if configured.runtime.homeDir == "" {
		return nil, errors.New("home directory is unavailable")
	}
	server.Args = append([]string(nil), server.Args...)
	server.Env = cloneEnv(server.Env)
	return &Installer{server: server, runtime: configured.runtime, system: configured.system}, nil
}

// Ensure plans and applies a selection in one call. Use Plan and Apply
// separately when showing a preview or requiring user confirmation.
func (i *Installer) Ensure(ctx context.Context, ids []ID, desired DesiredState, options PlanOptions) []Result {
	return i.Apply(ctx, i.Plan(ctx, ids, desired, options))
}
