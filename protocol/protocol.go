// Package protocol defines the stable JSON contract used by the companion
// binary and language wrappers.
package protocol

import detectharness "github.com/sairaph/detect-harness"

const Version = 1

type Operation string

const (
	Detect Operation = "detect"
	Render Operation = "render"
	Update Operation = "update"
)

// Request is read as one JSON document from stdin. Server secrets therefore do
// not appear in process arguments.
type Request struct {
	Version        int                          `json:"version"`
	Operation      Operation                    `json:"operation"`
	Server         *detectharness.StdioServer   `json:"server,omitempty"`
	Harness        detectharness.ID             `json:"harness,omitempty"`
	Harnesses      []detectharness.ID           `json:"harnesses,omitempty"`
	Desired        detectharness.DesiredState   `json:"desired,omitempty"`
	ConflictPolicy detectharness.ConflictPolicy `json:"conflictPolicy,omitempty"`
	DryRun         bool                         `json:"dryRun,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Response struct {
	Version    int                       `json:"version"`
	OK         bool                      `json:"ok"`
	Detections []detectharness.Detection `json:"detections,omitempty"`
	Config     string                    `json:"config,omitempty"`
	Changes    []detectharness.Change    `json:"changes,omitempty"`
	Results    []detectharness.Result    `json:"results,omitempty"`
	Error      *Error                    `json:"error,omitempty"`
}
