package detectharness

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

type probeResult struct {
	state    DetectionState
	evidence []string
	reason   string
}

func probePath(system hostSystem, candidate string) probeResult {
	if candidate == "" {
		return probeResult{state: Unavailable, reason: "client path is unavailable"}
	}
	_, err := system.Lstat(candidate)
	if err == nil {
		return probeResult{state: Detected, evidence: []string{candidate}}
	}
	if errors.Is(err, fs.ErrNotExist) {
		return probeResult{state: NotDetected}
	}
	return probeResult{state: Unavailable, reason: "cannot inspect " + candidate + ": " + err.Error()}
}

func combineProbes(results ...probeResult) probeResult {
	var evidence []string
	var unavailable string
	for _, result := range results {
		if result.state == Detected {
			evidence = append(evidence, result.evidence...)
		}
		if unavailable == "" && result.state == Unavailable {
			unavailable = result.reason
		}
	}
	if len(evidence) > 0 {
		sort.Strings(evidence)
		return probeResult{state: Detected, evidence: evidence}
	}
	if unavailable != "" {
		return probeResult{state: Unavailable, reason: unavailable}
	}
	return probeResult{state: NotDetected}
}

func probeCommand(system hostSystem, environment runtimeEnvironment, name string) probeResult {
	pathValue, exists := environment.lookupEnv("PATH")
	if !exists {
		return probeResult{state: Unavailable, reason: "PATH is not available for command detection"}
	}
	separator := string(filepath.ListSeparator)
	extensions := []string{""}
	if environment.platform == "windows" {
		separator = ";"
		if filepath.Ext(name) == "" {
			pathExt, ok := environment.lookupEnv("PATHEXT")
			if !ok {
				return probeResult{state: Unavailable, reason: "PATHEXT is required for Windows command detection"}
			}
			extensions = strings.Split(strings.ToLower(pathExt), ";")
		}
	}
	var firstError string
	for _, directory := range strings.Split(pathValue, separator) {
		if directory == "" {
			continue
		}
		for _, extension := range extensions {
			candidate := filepath.Join(directory, name+extension)
			info, err := system.Stat(candidate)
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
				continue
			}
			if err != nil {
				if firstError == "" {
					firstError = "cannot inspect command candidate " + candidate + ": " + err.Error()
				}
				continue
			}
			if !info.Mode().IsRegular() || (environment.platform != "windows" && info.Mode().Perm()&0o111 == 0) {
				continue
			}
			return probeResult{state: Detected, evidence: []string{candidate}}
		}
	}
	if firstError != "" {
		return probeResult{state: Unavailable, reason: firstError}
	}
	return probeResult{state: NotDetected}
}

func probeExtension(system hostSystem, environment runtimeEnvironment, prefix string) probeResult {
	directories := []string{
		environment.home(".vscode", "extensions"),
		environment.home(".vscode-insiders", "extensions"),
		environment.home(".cursor", "extensions"),
		environment.home(".windsurf", "extensions"),
		environment.home(".vscodium", "extensions"),
	}
	results := make([]probeResult, 0, len(directories))
	for _, directory := range directories {
		entries, err := system.ReadDir(directory)
		if errors.Is(err, fs.ErrNotExist) {
			results = append(results, probeResult{state: NotDetected})
			continue
		}
		if err != nil {
			results = append(results, probeResult{state: Unavailable, reason: "cannot inspect extensions in " + directory + ": " + err.Error()})
			continue
		}
		found := false
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), prefix) {
				results = append(results, probeResult{state: Detected, evidence: []string{filepath.Join(directory, entry.Name())}})
				found = true
				break
			}
		}
		if !found {
			results = append(results, probeResult{state: NotDetected})
		}
	}
	return combineProbes(results...)
}

// Detect probes every supported harness.
func (i *Installer) Detect(_ context.Context) []Detection {
	results := make([]Detection, 0, len(registry))
	for _, definition := range registry {
		probe := definition.detect(i.system, i.runtime)
		resolved := definition.config(i.system, i.runtime)
		result := Detection{
			Harness:  definition.Harness,
			State:    probe.state,
			Evidence: append([]string(nil), probe.evidence...),
			Reason:   probe.reason,
		}
		if resolved.reason != "" {
			result.ConfigError = resolved.reason
		} else {
			result.ConfigPath = resolved.path
		}
		results = append(results, result)
	}
	return results
}

// DetectHarnesses probes all built-in harnesses without requiring an MCP
// server definition. Zero-valued options use the current host environment.
func DetectHarnesses(ctx context.Context, options DetectOptions) ([]Detection, error) {
	configured := installerOptions{runtime: hostRuntime(), system: osSystem{}}
	if err := WithEnvironment(options)(&configured); err != nil {
		return nil, err
	}
	detector := &Installer{runtime: configured.runtime, system: configured.system}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return detector.Detect(ctx), nil
}
