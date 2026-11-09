package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/m0n0x41d/haft/internal/testpath"
)

// The CLI and both MCP profiles must give the same first rejection for a
// request with two invalid controls before creating a project store.
func TestT01ActualAdmissionOrderingAndCorrection(t *testing.T) {
	binary := testpath.Exe(filepath.Join(t.TempDir(), "haft10"))
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build haft10: %v: %s", err, output)
	}
	root := t.TempDir()
	invalid := `{"format":"haft.api/2","operation":"check","action":"observe","observation":null,"query":"foreign"}`
	var first map[string]any
	for index := range 16 {
		command := exec.Command(binary, "api", "--root", root, "--input", "-")
		command.Stdin = strings.NewReader(invalid)
		output, err := command.Output()
		if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
			t.Fatalf("CLI rejection %d: %v: %s", index, err, output)
		}
		var result map[string]any
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatal(err, string(output))
		}
		assertT01Admission(t, result, "required_field", "required_field: observation")
		if index == 0 {
			first = result
			continue
		}
		if !reflect.DeepEqual(first, result) {
			t.Fatalf("CLI diagnostic changed at repetition %d", index)
		}
	}
	for _, profile := range []struct{ name, tool, owner string }{
		{"default", "haft_check", "haft_read"},
		{"legacy", "haft", "haft"},
	} {
		t.Run(profile.name, func(t *testing.T) {
			longName := "caller-" + strings.Repeat("x", 600)
			calls := make([]string, 16)
			for index := range calls {
				calls[index] = fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, index+2, profile.tool, invalid)
			}
			calls = append(calls, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":{"format":"haft.api/2","operation":"recall"}}}`, len(calls)+2, longName))
			responses := t01MCPProcess(t, binary, root, profile.name, calls)
			if len(responses) != len(calls) {
				t.Fatalf("got %d MCP results for %d calls", len(responses), len(calls))
			}
			for index, result := range responses[:16] {
				structured := t01MCPError(t, result)
				assertT01Admission(t, structured, "required_field", "required_field: observation")
				if !reflect.DeepEqual(first, structured) {
					t.Fatalf("%s diagnostic differs from CLI at repetition %d", profile.name, index)
				}
			}
			correction := t01MCPError(t, responses[16])
			message := t01DiagnosticMessage(t, correction)
			want := "use advertised tool " + profile.owner
			if !strings.HasPrefix(message, "unknown_tool") || !strings.Contains(message, want) {
				t.Fatalf("missing correction %q: %q", want, message)
			}
			if strings.Index(message, want) > strings.Index(message, "caller-") {
				t.Fatalf("caller excerpt precedes correction: %q", message)
			}
			if strings.Contains(message, strings.Repeat("x", 200)) || len(message) > 300 {
				t.Fatalf("caller name was echoed without a bound: %d bytes", len(message))
			}
			encoded, err := json.Marshal(responses[16])
			if err != nil || len(encoded) > 8192 {
				t.Fatalf("MCP correction exceeded final budget: %v, %d bytes", err, len(encoded))
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, ".haft")); !os.IsNotExist(err) {
		t.Fatalf("invalid admission created .haft: %v", err)
	}
}

func t01MCPProcess(t *testing.T, binary, root, profile string, calls []string) []map[string]any {
	t.Helper()
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"admission-test","version":"1"}}}`
	wire := strings.Join(append([]string{initialize}, calls...), "\n") + "\n"
	command := exec.Command(binary, "serve", "--root", root, "--profile", profile)
	command.Stdin = strings.NewReader(wire)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("MCP %s process: %v: %s", profile, err, output)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	var initialized map[string]any
	if err := decoder.Decode(&initialized); err != nil || initialized["result"] == nil {
		t.Fatalf("MCP %s initialize: %v: %v", profile, err, initialized)
	}
	responses := make([]map[string]any, 0, len(calls))
	for range calls {
		var response map[string]any
		if err := decoder.Decode(&response); err != nil {
			t.Fatalf("MCP %s call: %v", profile, err)
		}
		responses = append(responses, response)
	}
	var extra map[string]any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("MCP %s extra output: %v: %v", profile, err, extra)
	}
	return responses
}

func t01MCPError(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	if !ok || result["isError"] != true {
		t.Fatalf("MCP call was not a rejected tool result: %v", response)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("MCP rejection lacks structuredContent: %v", result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("MCP rejection lacks text: %v", result)
	}
	var text map[string]any
	if err := json.Unmarshal([]byte(content[0].(map[string]any)["text"].(string)), &text); err != nil || !reflect.DeepEqual(structured, text) {
		t.Fatalf("MCP text/structured mismatch: %v", err)
	}
	return structured
}

func assertT01Admission(t *testing.T, result map[string]any, kind, prefix string) {
	t.Helper()
	if result["result_kind"] != kind || result["is_error"] != true {
		t.Fatalf("wrong admission result: %v", result)
	}
	if message := t01DiagnosticMessage(t, result); !strings.HasPrefix(message, prefix) {
		t.Fatalf("wrong first diagnostic: %q", message)
	}
}

func t01DiagnosticMessage(t *testing.T, result map[string]any) string {
	t.Helper()
	diagnostics, ok := result["diagnostics"].([]any)
	if !ok || len(diagnostics) == 0 {
		t.Fatalf("rejection lacks diagnostic: %v", result)
	}
	message, ok := diagnostics[0].(map[string]any)["message"].(string)
	if !ok {
		t.Fatalf("rejection lacks diagnostic message: %v", result)
	}
	return message
}
