package detectharness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
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
func (i *Installer) Plan(ctx context.Context, ids []ID, desired DesiredState, options PlanOptions) *Plan {
	if desired != Present && desired != Absent {
		return invalidSelectionPlan(ids, desired, "desired state must be present or absent")
	}
	policy := options.ConflictPolicy
	if policy == "" {
		policy = ConflictError
	}
	if policy != ConflictError && policy != ConflictReplace {
		return invalidSelectionPlan(ids, desired, "conflict policy must be error or replace")
	}
	scope, scopeErr := options.Scope.normalize()
	if scopeErr != nil {
		return invalidSelectionPlan(ids, desired, scopeErr.Error())
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
		if !found {
			plan.changes = append(plan.changes, plannedChange{Change: Change{HarnessID: id, Desired: desired, State: ChangeUnavailable, Reason: "unknown harness"}})
			continue
		}
		change := plannedChange{Change: Change{HarnessID: definition.ID, Name: definition.Name, Desired: desired}}
		if scope.Mode == ScopeProject {
			change.Scope = ScopeProject
			change.ScopeDir = scope.Dir
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
		if action == "" {
			change.State = ChangeNoop
			plan.changes = append(plan.changes, change)
			continue
		}
		change.State = ChangeReady
		change.Action = action
		change.Before = string(snapshot.raw)
		change.After = string(after)
		change.after = after
		plan.changes = append(plan.changes, change)
	}
	return plan
}

func invalidSelectionPlan(ids []ID, desired DesiredState, reason string) *Plan {
	plan := &Plan{changes: make([]plannedChange, 0, len(ids))}
	for _, id := range ids {
		plan.changes = append(plan.changes, plannedChange{Change: Change{HarnessID: CanonicalID(id), Desired: desired, State: ChangeUnavailable, Reason: reason}})
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
		result := Result{HarnessID: change.HarnessID, Name: change.Name, Path: change.Path, Desired: change.Desired, Action: change.Action, Scope: change.Scope, ScopeDir: change.ScopeDir}
		switch change.State {
		case ChangeNoop:
			result.State = ApplyNoop
		case ChangeConflict:
			result.State = ApplyConflict
			result.Reason = change.Reason
		case ChangeUnavailable:
			result.State = ApplySkipped
			result.Reason = change.Reason
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
