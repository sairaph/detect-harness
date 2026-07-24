package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	detectharness "github.com/sairaph/detect-harness"
	"github.com/sairaph/detect-harness/protocol"
)

func TestReadRequestIsStrictAndVersioned(t *testing.T) {
	request, err := readRequest(strings.NewReader(`{"version":1,"operation":"detect"}`))
	if err != nil || request.Operation != protocol.Detect {
		t.Fatalf("request = %#v, error = %v", request, err)
	}
	for _, input := range []string{
		`{"version":2,"operation":"detect"}`,
		`{"version":1,"operation":"detect","unknown":true}`,
		`{"version":1,"operation":"detect"} {}`,
	} {
		if _, err := readRequest(strings.NewReader(input)); err == nil {
			t.Fatalf("expected invalid request for %s", input)
		}
	}
}

func TestExecuteRender(t *testing.T) {
	response, err := execute(context.Background(), protocol.Request{
		Version:   protocol.Version,
		Operation: protocol.Render,
		Harness:   "vscode",
		Server: &detectharness.StdioServer{
			Name:    "example",
			Command: "/example",
			Args:    []string{"mcp"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK || !bytes.Contains([]byte(response.Config), []byte(`"type": "stdio"`)) {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestExecuteRejectsInvalidUpdateContract(t *testing.T) {
	server := &detectharness.StdioServer{Name: "example", Command: "/example"}
	for _, request := range []protocol.Request{
		{Version: 1, Operation: protocol.Update, Server: server, Harnesses: []detectharness.ID{detectharness.Cursor}},
		{Version: 1, Operation: protocol.Update, Server: server, Harnesses: []detectharness.ID{"unknown"}, Desired: detectharness.Present},
		{Version: 1, Operation: protocol.Update, Server: server, Harnesses: []detectharness.ID{detectharness.Cursor, detectharness.Cursor}, Desired: detectharness.Present},
	} {
		if _, err := execute(context.Background(), request); err == nil {
			t.Fatalf("expected request rejection: %#v", request)
		}
	}
}
