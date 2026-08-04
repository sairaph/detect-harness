package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	detectharness "github.com/sairaph/detect-harness"
	"github.com/sairaph/detect-harness/protocol"
)

const maximumRequestSize = 1 << 20

var buildVersion = "dev"

func main() {
	if len(os.Args) > 1 {
		if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
			fmt.Println(buildVersion)
			return
		}
		writeFailure("invalid_arguments", "requests must be supplied as JSON on stdin")
		os.Exit(2)
	}
	request, err := readRequest(os.Stdin)
	if err != nil {
		writeFailure("invalid_request", err.Error())
		os.Exit(2)
	}
	response, err := execute(context.Background(), request)
	if err != nil {
		writeFailure("operation_failed", err.Error())
		os.Exit(1)
	}
	if err := writeResponse(response); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func readRequest(reader io.Reader) (protocol.Request, error) {
	limited := io.LimitReader(reader, maximumRequestSize+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return protocol.Request{}, err
	}
	if len(raw) > maximumRequestSize {
		return protocol.Request{}, errors.New("request exceeds 1 MiB")
	}
	var request protocol.Request
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return protocol.Request{}, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return protocol.Request{}, errors.New("stdin must contain exactly one JSON document")
	}
	if request.Version != protocol.Version {
		return protocol.Request{}, fmt.Errorf("unsupported protocol version %d", request.Version)
	}
	return request, nil
}

func execute(ctx context.Context, request protocol.Request) (protocol.Response, error) {
	response := protocol.Response{Version: protocol.Version, OK: true}
	scope := detectharness.Scope{}
	if request.Scope != nil {
		scope = *request.Scope
	}
	switch request.Operation {
	case protocol.Detect:
		detections, err := detectharness.DetectHarnesses(ctx, detectharness.DetectOptions{Scope: scope})
		if err != nil {
			return protocol.Response{}, err
		}
		response.Detections = detections
		return response, nil
	case protocol.Render:
		if request.Server == nil || request.Harness == "" {
			return protocol.Response{}, errors.New("render requires server and harness")
		}
		config, err := detectharness.RenderConfigScoped(request.Harness, *request.Server, scope)
		if err != nil {
			return protocol.Response{}, err
		}
		response.Config = config
		return response, nil
	case protocol.Update:
		if request.Server == nil || len(request.Harnesses) == 0 {
			return protocol.Response{}, errors.New("update requires server and at least one harness")
		}
		if request.Desired != detectharness.Present && request.Desired != detectharness.Absent {
			return protocol.Response{}, errors.New("update desired must be present or absent")
		}
		if request.ConflictPolicy != "" && request.ConflictPolicy != detectharness.ConflictError && request.ConflictPolicy != detectharness.ConflictReplace {
			return protocol.Response{}, errors.New("conflictPolicy must be error or replace")
		}
		seen := make(map[detectharness.ID]struct{}, len(request.Harnesses))
		for _, id := range request.Harnesses {
			if !detectharness.IsSupported(id) {
				return protocol.Response{}, fmt.Errorf("unknown harness %q", id)
			}
			canonical := detectharness.CanonicalID(id)
			if _, duplicate := seen[canonical]; duplicate {
				return protocol.Response{}, fmt.Errorf("duplicate harness %q", canonical)
			}
			seen[canonical] = struct{}{}
		}
		installer, err := detectharness.New(*request.Server)
		if err != nil {
			return protocol.Response{}, err
		}
		plan := installer.Plan(ctx, request.Harnesses, request.Desired, detectharness.PlanOptions{ConflictPolicy: request.ConflictPolicy, Scope: scope})
		response.Changes = plan.Changes()
		if !request.DryRun {
			response.Results = installer.Apply(ctx, plan)
		}
		return response, nil
	default:
		return protocol.Response{}, fmt.Errorf("unknown operation %q", request.Operation)
	}
}

func writeFailure(code, message string) {
	_ = writeResponse(protocol.Response{Version: protocol.Version, OK: false, Error: &protocol.Error{Code: code, Message: message}})
}

func writeResponse(response protocol.Response) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(response)
}
