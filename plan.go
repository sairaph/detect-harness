package detectharness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
)

const maximumConfigSize = 8 << 20

type fileSnapshot struct {
	exists bool
	raw    []byte
	mode   fs.FileMode
}

type plannedChange struct {
	Change
	snapshot fileSnapshot
	after    []byte
}

func equalBytes(left, right []byte) bool { return bytes.Equal(left, right) }

func readSnapshot(system hostSystem, path string) (fileSnapshot, error) {
	info, err := system.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileSnapshot{}, nil
	}
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("inspect config: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fileSnapshot{}, errors.New("configuration file cannot be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return fileSnapshot{}, errors.New("configuration path is not a regular file")
	}
	if info.Size() > maximumConfigSize {
		return fileSnapshot{}, fmt.Errorf("configuration exceeds %d bytes", maximumConfigSize)
	}
	raw, err := system.ReadFile(path)
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("read config: %w", err)
	}
	if len(raw) > maximumConfigSize {
		return fileSnapshot{}, fmt.Errorf("configuration exceeds %d bytes", maximumConfigSize)
	}
	return fileSnapshot{exists: true, raw: raw, mode: info.Mode()}, nil
}

// Plan inspects selected harnesses and computes changes without writing files.
// It never returns nil: invalid input produces one unavailable change per
// selected harness whose Reason explains the rejection, so callers can render
// every outcome uniformly.
//
// Harnesses that merge additional configuration layers (for example OpenCode's
// config.json or $OPENCODE_CONFIG) are inspected for same-name entries
// that would shadow or outlive the managed entry. Under ConflictError such an
// entry is reported as a conflict; under ConflictReplace the change proceeds
// and Reason carries a warning naming the other layer.
func (i *Installer) Plan(ctx context.Context, ids []ID, desired DesiredState, options PlanOptions) *Plan {
	if desired != Present && desired != Absent {
		return invalidSelectionPlan(ids, desired, "desired state must be present or absent", options.Scope.Mode, options.Scope.Dir)
	}
	policy := options.ConflictPolicy
	if policy == "" {
		policy = ConflictError
	}
	if policy != ConflictError && policy != ConflictReplace {
		return invalidSelectionPlan(ids, desired, "conflict policy must be error or replace", options.Scope.Mode, options.Scope.Dir)
	}
	scope, scopeErr := options.Scope.normalize()
	if scopeErr != nil {
		return invalidSelectionPlan(ids, desired, scopeErr.Error(), ScopeGlobal, "")
	}
	if scope.Mode == ScopeProject {
		if err := projectDirError(i.system, scope.Dir); err != nil {
			return invalidSelectionPlan(ids, desired, err.Error(), ScopeProject, scope.Dir)
		}
	}
	plan := &Plan{changes: make([]plannedChange, 0, len(ids))}
	seen := make(map[ID]struct{}, len(ids))
	for _, id := range ids {
		canonical := CanonicalID(id)
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		definition, found := definitionFor(id)
		change := plannedChange{Change: Change{HarnessID: canonical, Name: definition.Name, Desired: desired}}
		if scope.Mode == ScopeProject {
			change.Scope = ScopeProject
			change.ScopeDir = scope.Dir
		}
		if !found {
			change.State = ChangeUnavailable
			change.Reason = "unknown harness"
			plan.changes = append(plan.changes, change)
			continue
		}
		if err := ctx.Err(); err != nil {
			change.State = ChangeUnavailable
			change.Reason = err.Error()
			plan.changes = append(plan.changes, change)
			continue
		}
		resolved := definition.resolveConfig(scope, i.system, i.runtime)
		if resolved.reason != "" {
			change.State = ChangeUnavailable
			change.Reason = resolved.reason
			plan.changes = append(plan.changes, change)
			continue
		}
		change.Path = resolved.path
		snapshot, err := readSnapshot(i.system, resolved.path)
		if err != nil {
			change.State = ChangeUnavailable
			change.Reason = err.Error()
			plan.changes = append(plan.changes, change)
			continue
		}
		change.snapshot = snapshot
		state, after, action, err := renderChange(definition, scope, snapshot.raw, i.server, desired, policy == ConflictReplace)
		if err != nil {
			change.State = ChangeUnavailable
			change.Reason = err.Error()
			plan.changes = append(plan.changes, change)
			continue
		}
		if state == ownershipForeign && policy == ConflictError {
			change.State = ChangeConflict
			change.Reason = "same-name entry does not match the configured MCP server"
			plan.changes = append(plan.changes, change)
			continue
		}
		notes, err := i.inspectLayers(definition, scope, resolved.path, desired)
		if err != nil {
			change.State = ChangeUnavailable
			change.Reason = err.Error()
			plan.changes = append(plan.changes, change)
			continue
		}
		warning := strings.Join(notes, "; ")
		if warning != "" && policy == ConflictError {
			change.State = ChangeConflict
			change.Reason = warning
			plan.changes = append(plan.changes, change)
			continue
		}
		if action == "" {
			change.State = ChangeNoop
			change.Reason = noopReason(desired)
			if warning != "" {
				change.Reason += "; " + warning
			}
			plan.changes = append(plan.changes, change)
			continue
		}
		change.State = ChangeReady
		change.Action = action
		change.Reason = warning
		change.Before = string(snapshot.raw)
		change.After = string(after)
		change.after = after
		plan.changes = append(plan.changes, change)
	}
	return plan
}

func noopReason(desired DesiredState) string {
	if desired == Absent {
		return "not registered"
	}
	return "already registered"
}

// inspectLayers checks every additional configuration layer of the harness for
// an entry with the managed server's name. It returns one human-readable note
// per entry that matters: any same-name entry when removing (the server would
// stay registered there), and a differing entry when adding (deep merging
// would combine or override the managed entry). Layers that cannot be
// inspected are errors so an unreadable layer never passes silently.
func (i *Installer) inspectLayers(definition harnessDefinition, scope Scope, target string, desired DesiredState) ([]string, error) {
	expected := serverEntry(definition.entry, i.server)
	var notes []string
	for _, layer := range definition.resolveLayers(scope, target, i.system, i.runtime) {
		raw := layer.raw
		if layer.path != "" {
			content, exists, err := readLayer(i.system, layer.path)
			if err != nil {
				return nil, fmt.Errorf("inspect %s: %w", layer.name, err)
			}
			if !exists {
				continue
			}
			raw = content
		}
		servers, err := layerServers(raw, definition.format, definition.topKey)
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", layer.name, err)
		}
		current, found := servers[i.server.Name]
		if !found {
			continue
		}
		switch {
		case desired == Absent:
			notes = append(notes, fmt.Sprintf("%s also defines %q, so the server stays registered there after removal", layer.name, i.server.Name))
		case semanticEqual(current, expected):
			continue
		case layer.overrides:
			notes = append(notes, fmt.Sprintf("%s defines a different %q entry and overrides this file", layer.name, i.server.Name))
		default:
			notes = append(notes, fmt.Sprintf("%s defines a different %q entry that is deep-merged beneath this file", layer.name, i.server.Name))
		}
	}
	return notes, nil
}

// readLayer reads an additional configuration layer for inspection. Unlike
// readSnapshot it follows symbolic links, because the harness itself does and
// the layer is never written.
func readLayer(system hostSystem, path string) ([]byte, bool, error) {
	info, err := system.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("configuration path is not a regular file")
	}
	if info.Size() > maximumConfigSize {
		return nil, false, fmt.Errorf("configuration exceeds %d bytes", maximumConfigSize)
	}
	raw, err := system.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func invalidSelectionPlan(ids []ID, desired DesiredState, reason string, scope ScopeMode, scopeDir string) *Plan {
	plan := &Plan{changes: make([]plannedChange, 0, len(ids))}
	seen := make(map[ID]struct{}, len(ids))
	for _, id := range ids {
		canonical := CanonicalID(id)
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}
		definition, _ := definitionFor(canonical)
		plan.changes = append(plan.changes, plannedChange{Change: Change{HarnessID: canonical, Name: definition.Name, Desired: desired, State: ChangeUnavailable, Reason: reason, Scope: scope, ScopeDir: scopeDir}})
	}
	return plan
}

// Apply writes ready changes from a plan and returns one result per change.
func (i *Installer) Apply(ctx context.Context, plan *Plan) []Result {
	if plan == nil {
		return nil
	}
	results := make([]Result, 0, len(plan.changes))
	for _, change := range plan.changes {
		result := Result{HarnessID: change.HarnessID, Name: change.Name, Path: change.Path, Desired: change.Desired, Action: change.Action, Reason: change.Reason, Scope: change.Scope, ScopeDir: change.ScopeDir}
		switch change.State {
		case ChangeNoop:
			result.State = ApplyNoop
		case ChangeConflict:
			result.State = ApplyConflict
		case ChangeUnavailable:
			result.State = ApplySkipped
		case ChangeReady:
			if err := ctx.Err(); err != nil {
				result.State = ApplyFailed
				result.Reason = err.Error()
			} else if err := i.system.WriteFileAtomic(change.Path, change.snapshot, change.after); err != nil {
				result.State = ApplyFailed
				result.Reason = err.Error()
			} else {
				result.State = Applied
			}
		default:
			result.State = ApplyFailed
			result.Reason = "plan contains an invalid change state"
		}
		results = append(results, result)
	}
	return results
}
