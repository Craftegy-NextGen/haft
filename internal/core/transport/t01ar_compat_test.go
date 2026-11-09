package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/m0n0x41d/haft/internal/core/app"
	"github.com/m0n0x41d/haft/internal/core/carrier"
	"github.com/m0n0x41d/haft/internal/core/change"
	"github.com/m0n0x41d/haft/internal/core/check"
	"github.com/m0n0x41d/haft/internal/core/code"
	"github.com/m0n0x41d/haft/internal/core/delivery"
	"github.com/m0n0x41d/haft/internal/core/store"
	"github.com/m0n0x41d/haft/internal/testpath"
)

// Build the real entrypoint so the CLI leg uses its public decoder, validator
// and delivery path. The test-owned binary and project never touch host state.
func t01arCLI(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := testpath.Exe(filepath.Join(t.TempDir(), "haft10"))
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/haft10")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build haft10: %v: %s", err, output)
	}
	return binary
}

func t01arCallCLI(t *testing.T, binary, root string, request any) delivery.Response {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "api", "--root", root, "--input", "-")
	cmd.Stdin = bytes.NewReader(raw)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("haft10 api %s: %v: %s", raw, err, output)
	}
	var result delivery.Response
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("CLI returned non-JSON %q: %v", output, err)
	}
	return result
}

func t01arPart(t *testing.T, client *t01aProtocolClient, result delivery.Response, name string, target any) {
	t.Helper()
	part, found := t01aParts(t, client, result)[name]
	if !found {
		t.Fatalf("%s absent from %s result", name, result.Kind)
	}
	raw := t01aBytesPart(t, client, result.Delivery.ReadTool, part)
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

func t01arTransactionID(t *testing.T, result delivery.Response) string {
	t.Helper()
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("receipt data is not an object: %+v", result)
	}
	id, ok := data["transaction_id"].(string)
	if !ok || strings.TrimSpace(id) == "" {
		t.Fatalf("receipt lacks a nonempty transaction_id string: %+v", result)
	}
	return id
}

func t01arProject(t *testing.T) (app.Service, *t01aProtocolClient) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"go.mod", "order.go", "policy.go", "order_test.go"} {
		raw, err := os.ReadFile(filepath.Join("..", "testdata", "order", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	service := app.Service{Root: root}
	client := t01aStartClient(t, service, ProfileDefault)
	client.list(t)
	for _, item := range []struct{ file, key string }{{"terms.md", "terms"}, {"spec.md", "spec"}} {
		raw, err := os.ReadFile(filepath.Join("..", "testdata", "order", item.file))
		if err != nil {
			t.Fatal(err)
		}
		request := app.Request{Format: delivery.Format, Operation: "remember", RequestID: "t01ar-" + item.key, Carrier: string(raw)}
		if item.key == "terms" {
			request.Action = "terms"
		}
		result := client.mustCall(t, "haft_write", request)
		if result.Kind != "written" || result.IsError {
			t.Fatalf("seed %s: %+v", item.key, result)
		}
	}
	return service, client
}

func t01arActualRun(t *testing.T, root string, expected check.Contract, command []string, config code.Config) check.ObservationInput {
	t.Helper()
	environment, err := code.GoTestEnvironment(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=")
	for key, value := range environment {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("real Go test timed out: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("real Go test %q: %v: %s %s", command, err, stdout.String(), stderr.String())
	}
	exit := 0
	input := check.ObservationInput{Expected: expected, Observed: check.Run{Started: true, ExitCode: &exit, Command: command, Selector: expected.Selector, Basis: expected.Basis, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}}
	classified := check.GoTestObservation(input)
	if classified.Status != check.Passed || classified.ReasonCode != "selected_test_passed" {
		t.Fatalf("real selected test did not pass: %s/%s: %s", classified.Status, classified.ReasonCode, stdout.String())
	}
	return input
}

func t01arBasis(t *testing.T, result delivery.Response, want string) {
	t.Helper()
	if result.Kind != check.Passed || result.IsError {
		t.Fatalf("observation outcome: %+v", result)
	}
	data, ok := result.Data.(map[string]any)
	if !ok || data["current_basis"] != want {
		t.Fatalf("current_basis=%v, want %s: %+v", data, want, result)
	}
}

func TestT01ARCustomCodeConfigPrepareRunObserveAcrossSurfaces(t *testing.T) {
	if testing.Short() {
		t.Skip("real uncached Go run and CLI build")
	}
	service, defaultClient := t01arProject(t)
	legacyClient := t01aStartClient(t, service, ProfileLegacy)
	legacyClient.list(t)
	binary := t01arCLI(t)
	// An explicit non-default ignore policy participates in the dependency
	// basis even before its generated path exists in this project.
	config := code.Config{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Toolchain: runtime.Version(), IncludeTests: true, IgnorePatterns: []string{"generated.go"}}
	prepare := app.Request{Format: delivery.Format, Operation: "check", Action: "prepare", Ref: "spec:order-cancel#total-preserved", CheckRef: "pbt:order_test.go::TestCancelPreservesTotal", Scope: "1000 generated new/paid cases, seed 23", CodeConfig: &config}
	prepared := defaultClient.mustCall(t, "haft_check", prepare)
	if prepared.Kind != "prepared" || prepared.IsError {
		t.Fatalf("default prepare: %+v", prepared)
	}
	var expected check.Contract
	var command []string
	t01arPart(t, defaultClient, prepared, "expected", &expected)
	t01arPart(t, defaultClient, prepared, "command", &command)
	for name, alternative := range map[string]delivery.Response{
		"CLI":    t01arCallCLI(t, binary, service.Root, prepare),
		"legacy": legacyClient.mustCall(t, "haft", prepare),
	} {
		if alternative.Kind != "prepared" || alternative.IsError || alternative.Basis["code_basis"] != prepared.Basis["code_basis"] {
			t.Fatalf("%s did not prepare the same custom code basis: %+v", name, alternative)
		}
	}
	observed := t01arActualRun(t, service.Root, expected, command, config)
	observe := app.Request{Format: delivery.Format, Operation: "check", Action: "observe", Observation: &observed, CodeConfig: &config}
	t01arBasis(t, t01arCallCLI(t, binary, service.Root, observe), "same")
	t01arBasis(t, defaultClient.mustCall(t, "haft_check", observe), "same")
	t01arBasis(t, legacyClient.mustCall(t, "haft", observe), "same")
	observe.CodeConfig = nil
	t01arBasis(t, defaultClient.mustCall(t, "haft_check", observe), "changed")
	observe.CodeConfig = &config
	policy := filepath.Join(service.Root, "policy.go")
	raw, err := os.ReadFile(policy)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(raw), `status == "new" || status == "paid"`, `status == "new"`, 1)
	if changed == string(raw) {
		t.Fatal("dependency mutation missed the fixture expression")
	}
	if err := os.WriteFile(policy, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	t01arBasis(t, t01arCallCLI(t, binary, service.Root, observe), "changed")
	t01arBasis(t, defaultClient.mustCall(t, "haft_check", observe), "changed")
	t01arBasis(t, legacyClient.mustCall(t, "haft", observe), "changed")
}

func TestT01ARFrozenB1OptionalNullReplayAndControls(t *testing.T) {
	root, basis, request, frozen := t01aFrozenB1(t)
	if strings.TrimSpace(basis.TransactionID) == "" {
		t.Fatal("frozen B1 basis lacks a transaction ID")
	}
	defaultClient := t01aStartClient(t, app.Service{Root: root}, ProfileDefault)
	defaultClient.list(t)
	legacyClient := t01aStartClient(t, app.Service{Root: root}, ProfileLegacy)
	legacyClient.list(t)
	base, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var nullable map[string]any
	if err := json.Unmarshal(base, &nullable); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"snapshots", "expected_heads", "expected_target_digests", "retain"} {
		nullable[field] = nil
	}
	cliReplay := t01arCallCLI(t, t01arCLI(t), root, nullable)
	if cliReplay.Kind != "replayed" || cliReplay.IsError || cliReplay.Basis["memory_generation"] != basis.Generation || t01arTransactionID(t, cliReplay) != basis.TransactionID {
		t.Fatalf("CLI changed frozen B1 optional-null receipt: %+v", cliReplay)
	}
	for name, client := range map[string]*t01aProtocolClient{"haft_write": defaultClient, "haft": legacyClient} {
		replayed := client.mustCall(t, name, nullable)
		if replayed.Kind != "replayed" || replayed.IsError || replayed.Basis["memory_generation"] != basis.Generation {
			t.Fatalf("%s rejected B1 nil-able optional replay: %+v", name, replayed)
		}
		if t01arTransactionID(t, replayed) != basis.TransactionID {
			t.Fatalf("%s changed frozen B1 receipt: %+v", name, replayed)
		}
	}
	for _, attempt := range []struct {
		name  string
		field string
		value any
		want  string
	}{
		{"required-null", "carrier", nil, "invalid_json"},
		{"scalar-null", "request_id", nil, "invalid_json"},
		{"unknown-control", "unknown_control", true, "unknown_field"},
		{"wrong-branch", "query", "not a write field", "unsupported_field"},
	} {
		invalid := map[string]any{"format": delivery.Format, "operation": "remember", "request_id": basis.RequestID, "carrier": request.Carrier}
		invalid[attempt.field] = attempt.value
		for name, client := range map[string]*t01aProtocolClient{"haft_write": defaultClient, "haft": legacyClient} {
			result, rpcErr := client.call(t, name, invalid)
			if rpcErr != nil || result.Kind != attempt.want || !result.IsError {
				t.Fatalf("%s %s: want %s, got %+v / %+v", name, attempt.name, attempt.want, result, rpcErr)
			}
		}
	}
	t01aAssertB1Read(t, defaultClient, "haft_read", basis, frozen)
	t01aAssertB1Read(t, legacyClient, "haft", basis, frozen)
	for relative, original := range frozen {
		current, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil || !bytes.Equal(current, original) {
			t.Fatalf("B1 durable file changed: %s: %v", relative, err)
		}
	}
	notes, err := os.ReadDir(filepath.Join(root, ".haft", "notes"))
	if err != nil || len(notes) != 1 {
		t.Fatalf("B1 replay duplicated note: %d notes, %v", len(notes), err)
	}
}

// These are new candidate writes, separate from the frozen B1 note fixture.
// They prove the admitted fields reach current application behavior; they do
// not claim historical B1 bytes for terms, changes or retained results.
func TestT01ARTermsChangeAndRetainReachApplication(t *testing.T) {
	service, defaultClient := t01arProject(t)
	legacyClient := t01aStartClient(t, service, ProfileLegacy)
	legacyClient.list(t)

	termsBytes, err := os.ReadFile(filepath.Join("..", "testdata", "order", "terms.md"))
	if err != nil {
		t.Fatal(err)
	}
	terms := app.Request{Format: delivery.Format, Operation: "remember", Action: "terms", RequestID: "t01ar-terms", Carrier: string(termsBytes)}
	termsReplay := legacyClient.mustCall(t, "haft", terms)
	if termsReplay.Kind != "replayed" || termsReplay.IsError {
		t.Fatalf("terms replay through legacy profile: %+v", termsReplay)
	}
	t01arTransactionID(t, termsReplay)
	if _, err := os.Stat(filepath.Join(service.Root, ".haft", "specs", "terms.md")); err != nil {
		t.Fatalf("terms publication missing: %v", err)
	}

	// Retain an exact transient result part through the public request. The
	// payload digest belongs to the logical retain request, not the augmented
	// carrier bytes that contain its captured attachment.
	structural := defaultClient.mustCall(t, "haft_check", app.Request{Format: delivery.Format, Operation: "check", Action: "structural", Strict: true})
	if structural.Kind != "structurally_valid" || structural.IsError {
		t.Fatalf("structural fixture: %+v", structural)
	}
	resultPart, present := t01aParts(t, defaultClient, structural)["result"]
	if !present {
		t.Fatal("transient result part absent")
	}
	resultBytes := t01aBytesPart(t, defaultClient, structural.Delivery.ReadTool, resultPart)
	noteID := "note-20260925-01a0beef"
	note := app.Request{Format: delivery.Format, Operation: "remember", RequestID: "t01ar-retain-once", Carrier: "---\nkind: note\nid: " + noteID + "\ntitle: Retained bounded result\nabout: domain:T01AR.Retain\n---\nThe result bytes follow as an attachment.\n", Retain: []app.Retention{{Ref: resultPart.Request.Ref, Part: "result"}}}
	retained := defaultClient.mustCall(t, "haft_write", note)
	if retained.Kind != "written" || retained.IsError {
		t.Fatalf("retain write: %+v", retained)
	}
	replayed := legacyClient.mustCall(t, "haft", note)
	if replayed.Kind != "replayed" || replayed.IsError || t01arTransactionID(t, replayed) != t01arTransactionID(t, retained) {
		t.Fatalf("retain retry changed publication: %+v / %+v", retained, replayed)
	}
	saved, err := os.ReadFile(filepath.Join(service.Root, ".haft", "notes", noteID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(saved, []byte("```json\n"))
	if start < 0 {
		t.Fatal("retained attachment missing from carrier")
	}
	start += len("```json\n")
	end := bytes.Index(saved[start:], []byte("\n```"))
	if end < 0 {
		t.Fatal("retained attachment fence incomplete")
	}
	var attachment struct {
		Format string `json:"format"`
		Ref    string `json:"result_ref"`
		Part   string `json:"part"`
		Digest string `json:"digest"`
		Raw    []byte `json:"bytes_base64"`
	}
	if err := json.Unmarshal(saved[start:start+end], &attachment); err != nil {
		t.Fatal(err)
	}
	if attachment.Format != "haft.retained-part/1" || attachment.Ref != resultPart.Request.Ref || attachment.Part != "result" || attachment.Digest != carrier.Digest(resultBytes) || !bytes.Equal(attachment.Raw, resultBytes) {
		t.Fatalf("retained attachment differs from selected result: %+v", attachment)
	}

	view, err := (store.Store{Root: service.Root}).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var specification carrier.Document
	for _, document := range view.Documents {
		if document.Record.Kind == "spec" {
			specification = document
		}
	}
	if specification.Record.ID == "" {
		t.Fatal("seeded specification absent")
	}
	base := specification.Record.ID + "@" + specification.Edition
	updated := specification.Record.Claims[0]
	updated.Text += " This fixture records one extra review qualification."
	changeID := "chg-20260925-01a0beef"
	proposal := change.Change{Format: change.Format, ID: changeID, ChangeKey: changeID, Title: "Clarify one fixture claim", Intent: "Exercise change metadata admission", State: "open", CreatedAt: "2026-09-25T00:00:00Z", Patches: []change.SectionPatch{{Base: base, Operations: []change.Operation{{Op: "MODIFIED", ClaimID: updated.ID, Claim: &updated, Reason: "Clarify one bounded fixture claim"}}}}}
	changeBytes, err := change.Encode(proposal, []byte("A bounded candidate change, not an accepted norm.\n"))
	if err != nil {
		t.Fatal(err)
	}
	created := defaultClient.mustCall(t, "haft_change", app.Request{Format: delivery.Format, Operation: "change", Action: "create", RequestID: "t01ar-change-create", Carrier: string(changeBytes)})
	if created.Kind != "written" || created.IsError {
		t.Fatalf("change create: %+v", created)
	}
	preview := legacyClient.mustCall(t, "haft", app.Request{Format: delivery.Format, Operation: "change", Action: "preview", Ref: changeID})
	if preview.Kind != "ready" || preview.IsError || preview.Basis["preview_digest"] == "" || preview.Basis["change_ref"] == "" {
		t.Fatalf("change preview: %+v", preview)
	}
	successorID := "spec-20260925-01a0beef"
	apply := app.Request{Format: delivery.Format, Operation: "change", Action: "apply", Ref: preview.Basis["change_ref"], RequestID: "t01ar-change-apply", ExpectedGeneration: preview.Basis["memory_generation"], PreviewDigest: preview.Basis["preview_digest"], Metadata: map[string]change.Metadata{base: {ID: successorID, CreatedAt: "2026-09-25T00:00:01Z", Origin: "agent_proposal", Status: "proposed"}}}
	applied := defaultClient.mustCall(t, "haft_change", apply)
	if applied.Kind != "written" || applied.IsError {
		t.Fatalf("change apply with metadata: %+v", applied)
	}
	applyReplay := legacyClient.mustCall(t, "haft", apply)
	if applyReplay.Kind != "replayed" || applyReplay.IsError || t01arTransactionID(t, applyReplay) != t01arTransactionID(t, applied) {
		t.Fatalf("change apply retry changed publication: %+v / %+v", applied, applyReplay)
	}
	successor := defaultClient.mustCall(t, "haft_read", app.Request{Format: delivery.Format, Operation: "recall", Ref: successorID})
	if successor.Kind != "found" || successor.IsError {
		t.Fatalf("metadata-selected successor ID absent: %+v", successor)
	}
}
