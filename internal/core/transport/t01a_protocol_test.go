package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/m0n0x41d/haft/internal/core/app"
	"github.com/m0n0x41d/haft/internal/core/carrier"
	"github.com/m0n0x41d/haft/internal/core/delivery"
)

type t01aProtocolClient struct {
	in       *io.PipeWriter
	out      *bufio.Reader
	finished chan error
	nextID   int
	tools    map[string]bool
}

func t01aStartClient(t *testing.T, service app.Service, profile string) *t01aProtocolClient {
	t.Helper()
	input, writer := io.Pipe()
	reader, output := io.Pipe()
	bufferedOutput := bufio.NewReader(reader)
	finished := make(chan error, 1)
	client := &t01aProtocolClient{in: writer, out: bufferedOutput, finished: finished, tools: map[string]bool{}}
	server := Server{Service: service, Version: "T01A-test", Profile: profile}
	go func() {
		ctx := context.Background()
		client.finished <- server.Serve(ctx, input, output)
		_ = output.Close()
	}()
	t.Cleanup(func() {
		_ = writer.Close()
		if err := <-client.finished; err != nil {
			t.Error(err)
		}
		_ = reader.Close()
	})
	init := map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "T01A protocol test", "version": "1"},
	}
	result, rpcErr := client.request(t, "initialize", init)
	if rpcErr != nil {
		t.Fatalf("initialize: %+v", rpcErr)
	}
	var initialized struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(result, &initialized); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(initialized.Instructions, "next_request") {
		t.Fatal("server instructions do not explain returned reads")
	}
	return client
}

func (client *t01aProtocolClient) request(t *testing.T, method string, params any) (json.RawMessage, *rpcError) {
	t.Helper()
	client.nextID++
	envelope := map[string]any{"jsonrpc": "2.0", "id": client.nextID, "method": method, "params": params}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	frame := string(raw)
	if _, err := fmt.Fprintln(client.in, frame); err != nil {
		t.Fatal(err)
	}
	line, err := client.out.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(line, &reply); err != nil {
		t.Fatalf("invalid MCP response %q: %v", line, err)
	}
	if len(line) > delivery.Budget && reply.Error != nil {
		t.Fatalf("serialized MCP error is %d bytes", len(line))
	}
	return reply.Result, reply.Error
}

func (client *t01aProtocolClient) list(t *testing.T) []struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations"`
} {
	t.Helper()
	result, rpcErr := client.request(t, "tools/list", map[string]any{})
	if rpcErr != nil {
		t.Fatalf("tools/list: %+v", rpcErr)
	}
	var listed struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
			Annotations map[string]any `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &listed); err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		client.tools[tool.Name] = true
	}
	return listed.Tools
}

func (client *t01aProtocolClient) call(t *testing.T, tool string, args any) (delivery.Response, *rpcError) {
	t.Helper()
	result, rpcErr := client.request(t, "tools/call", map[string]any{"name": tool, "arguments": args})
	if rpcErr != nil {
		return delivery.Response{}, rpcErr
	}
	if len(result) > delivery.Budget {
		t.Fatalf("serialized tools/call result for %s is %d bytes", tool, len(result))
	}
	var wire struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Structured delivery.Response `json:"structuredContent"`
		IsError    bool              `json:"isError"`
	}
	if err := json.Unmarshal(result, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Content) != 1 {
		t.Fatalf("expected one text representation, got %d", len(wire.Content))
	}
	var textValue delivery.Response
	textBytes := []byte(wire.Content[0].Text)
	if err := json.Unmarshal(textBytes, &textValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wire.Structured, textValue) || wire.IsError != wire.Structured.IsError {
		t.Fatal("text/structured/error parity changed")
	}
	return wire.Structured, nil
}

func (client *t01aProtocolClient) mustCall(t *testing.T, tool string, args any) delivery.Response {
	t.Helper()
	if !client.tools[tool] {
		t.Fatalf("attempted %s, which tools/list did not advertise", tool)
	}
	result, rpcErr := client.call(t, tool, args)
	if rpcErr != nil {
		t.Fatalf("%s rejected %+v: %+v", tool, args, rpcErr)
	}
	return result
}

func t01aRead(t *testing.T, client *t01aProtocolClient, tool string, request delivery.Request) delivery.Response {
	t.Helper()
	if request.Format != delivery.Format || request.Operation != "read" {
		t.Fatalf("returned request is not an executable haft.api/2 read: %+v", request)
	}
	result := client.mustCall(t, tool, request)
	if result.Delivery.ReadTool != tool {
		t.Fatalf("returned read tool %q differs from advertised %q", result.Delivery.ReadTool, tool)
	}
	return result
}

func t01aParts(t *testing.T, client *t01aProtocolClient, result delivery.Response) map[string]delivery.Descriptor {
	t.Helper()
	tool := result.Delivery.ReadTool
	if !client.tools[tool] || result.Delivery.Catalog == nil {
		t.Fatalf("unusable parts request: %+v", result.Delivery)
	}
	parts := map[string]delivery.Descriptor{}
	for request := result.Delivery.Catalog; request != nil; {
		page := t01aRead(t, client, tool, *request)
		if page.Delivery.Encoding != "part_directory" {
			t.Fatalf("parts request did not return a directory: %+v", page)
		}
		var data struct {
			Parts []delivery.Descriptor `json:"parts"`
		}
		raw, err := json.Marshal(page.Data)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		for _, part := range data.Parts {
			parts[part.Name] = part
		}
		request = page.Delivery.Next
	}
	return parts
}

func t01aTextPart(t *testing.T, client *t01aProtocolClient, tool string, descriptor delivery.Descriptor) ([]byte, int) {
	t.Helper()
	var full []byte
	pages := 0
	for request := &descriptor.Request; request != nil; {
		page := t01aRead(t, client, tool, *request)
		data, ok := page.Data.(map[string]any)
		if !ok || page.Delivery.Encoding != "utf-8" {
			t.Fatalf("text part was not readable: %+v", page)
		}
		chunk, ok := data["text"].(string)
		if !ok || page.Delivery.Offset != len(full) {
			t.Fatalf("text page changed offset or shape: %+v", page)
		}
		full = append(full, chunk...)
		pages++
		request = page.Delivery.Next
	}
	if len(full) != descriptor.Bytes {
		t.Fatalf("part incomplete: got %d of %d bytes", len(full), descriptor.Bytes)
	}
	return full, pages
}

func t01aBytesPart(t *testing.T, client *t01aProtocolClient, tool string, descriptor delivery.Descriptor) []byte {
	t.Helper()
	request := descriptor.Request
	request.View = "bytes"
	var full []byte
	for {
		page := t01aRead(t, client, tool, request)
		data, ok := page.Data.(map[string]any)
		if !ok || page.Delivery.Encoding != "base64" {
			t.Fatalf("byte part was not readable: %+v", page)
		}
		encoded, ok := data["bytes_base64"].(string)
		if !ok || page.Delivery.Offset != len(full) {
			t.Fatalf("byte page changed offset or shape: %+v", page)
		}
		chunk, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		full = append(full, chunk...)
		if page.Delivery.Next == nil {
			if !page.Delivery.Complete {
				t.Fatalf("byte part ended incomplete: %+v", page.Delivery)
			}
			break
		}
		request = *page.Delivery.Next
	}
	if len(full) != descriptor.Bytes || carrier.Digest(full) != descriptor.Digest {
		t.Fatalf("byte part changed: %s (%d bytes)", descriptor.Name, len(full))
	}
	return full
}

type t01aB1Basis struct {
	ProducerCommit       string            `json:"producer_commit"`
	ProducerBinarySHA256 string            `json:"producer_binary_sha256"`
	RequestFile          string            `json:"request_file"`
	RecordID             string            `json:"record_id"`
	RequestID            string            `json:"request_id"`
	PayloadDigest        string            `json:"payload_digest"`
	Generation           string            `json:"generation"`
	TransactionID        string            `json:"transaction_id"`
	Files                map[string]string `json:"files"`
}

func t01aFrozenB1(t *testing.T) (string, t01aB1Basis, app.Request, map[string][]byte) {
	t.Helper()
	fixtureRoot := filepath.Join("testdata", "b1-3d3ad688")
	basisBytes, err := os.ReadFile(filepath.Join(fixtureRoot, "basis.json"))
	if err != nil {
		t.Fatal(err)
	}
	var basis t01aB1Basis
	if err := json.Unmarshal(basisBytes, &basis); err != nil {
		t.Fatal(err)
	}
	if basis.ProducerCommit != "3d3ad688944ed7587ad38f406d7efa8999fa758f" || basis.ProducerBinarySHA256 != "a8430ba6ec9a4d1124a003ca080f127e86bd209cdb600314685bceccd2a359fe" {
		t.Fatalf("unexpected B1 fixture producer basis: %+v", basis)
	}
	requestBytes, err := os.ReadFile(filepath.Join(fixtureRoot, basis.RequestFile))
	if err != nil {
		t.Fatal(err)
	}
	request, err := DecodeRequest(requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if request.RequestID != basis.RequestID || request.Format != delivery.Format || request.Operation != "remember" {
		t.Fatalf("fixture request differs from producer record: %+v", request)
	}
	root := t.TempDir()
	frozen := map[string][]byte{}
	for relative, digest := range basis.Files {
		if filepath.IsAbs(relative) || !strings.HasPrefix(relative, ".haft/") || strings.Contains(relative, "..") {
			t.Fatalf("unsafe B1 fixture path: %s", relative)
		}
		fixturePath := filepath.Join(fixtureRoot, relative)
		raw, err := os.ReadFile(fixturePath)
		if err != nil {
			t.Fatal(err)
		}
		if carrier.Digest(raw) != digest {
			t.Fatalf("frozen B1 file changed: %s", relative)
		}
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		frozen[relative] = raw
	}
	if len(frozen) != 6 {
		t.Fatalf("B1 fixture lost a record, snapshot or transaction file: %d", len(frozen))
	}
	return root, basis, request, frozen
}

func t01aAssertB1Read(t *testing.T, client *t01aProtocolClient, tool string, basis t01aB1Basis, frozen map[string][]byte) {
	t.Helper()
	read := app.Request{Format: delivery.Format, Operation: "recall", Ref: basis.RecordID}
	result := client.mustCall(t, tool, read)
	if result.Kind != "found" || result.IsError || result.Delivery.ReadTool != tool {
		t.Fatalf("B1 record not readable through %s: %+v", tool, result)
	}
	parts := t01aParts(t, client, result)
	recordDescriptor, recordPresent := parts["record"]
	carrierDescriptor, carrierPresent := parts["carrier"]
	snapshotDescriptor, snapshotPresent := parts["snapshot"]
	if !recordPresent || !carrierPresent || !snapshotPresent {
		t.Fatalf("B1 record, carrier or snapshot part missing through %s: %v", tool, parts)
	}
	record := t01aRead(t, client, tool, recordDescriptor.Request)
	recordData, ok := record.Data.(map[string]any)
	if !ok {
		t.Fatalf("B1 record detail malformed through %s: %+v", tool, record)
	}
	receipt, ok := recordData["write_receipt"].(map[string]any)
	if !ok || receipt["request_id"] != basis.RequestID || receipt["payload_digest"] != basis.PayloadDigest {
		t.Fatalf("B1 receipt changed through %s: %+v", tool, recordData["write_receipt"])
	}
	recordBytes := t01aBytesPart(t, client, tool, carrierDescriptor)
	recordPath := filepath.ToSlash(filepath.Join(".haft", "notes", basis.RecordID+".md"))
	if !bytes.Equal(recordBytes, frozen[recordPath]) {
		t.Fatalf("B1 carrier bytes changed through %s", tool)
	}
	snapshotBytes := t01aBytesPart(t, client, tool, snapshotDescriptor)
	snapshotName := strings.TrimPrefix(snapshotDescriptor.Digest, "sha256:") + ".json"
	snapshotPath := filepath.ToSlash(filepath.Join(".haft", "editions", "sha256", snapshotName))
	if !bytes.Equal(snapshotBytes, frozen[snapshotPath]) {
		t.Fatalf("B1 snapshot bytes changed through %s", tool)
	}
	stagedRoot := filepath.ToSlash(filepath.Join(".haft", "transactions", basis.TransactionID, "staged"))
	if !bytes.Equal(snapshotBytes, frozen[filepath.ToSlash(filepath.Join(stagedRoot, "000000"))]) || !bytes.Equal(recordBytes, frozen[filepath.ToSlash(filepath.Join(stagedRoot, "000001"))]) {
		t.Fatalf("B1 staged transaction outputs differ from served bytes through %s", tool)
	}
}

func TestT01ATaskToolsProtocol(t *testing.T) {
	root := t.TempDir()
	service := app.Service{Root: root, SourceRoot: "../source/testdata/pin-a", SourceRepository: "fixture://t01a"}
	client := t01aStartClient(t, service, ProfileDefault)
	tools := client.list(t)
	wantNames := []string{"haft_read", "haft_write", "haft_change", "haft_check", "haft_fpf"}
	gotNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		gotNames = append(gotNames, tool.Name)
		if !strings.Contains(tool.Description, "next_request") {
			t.Fatalf("%s description does not explain progressive reading", tool.Name)
		}
		if tool.InputSchema["additionalProperties"] != false {
			t.Fatalf("%s root schema is open", tool.Name)
		}
		branches, ok := tool.InputSchema["oneOf"].([]any)
		if !ok || len(branches) == 0 {
			t.Fatalf("%s lacks action branches", tool.Name)
		}
		for _, variant := range branches {
			branch := variant.(map[string]any)
			if branch["additionalProperties"] != false {
				t.Fatalf("%s has an open action branch: %v", tool.Name, branch["title"])
			}
			if !strings.Contains(branch["description"].(string), "Effect:") {
				t.Fatalf("%s omits branch effect", tool.Name)
			}
		}
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("default profile advertised %v", gotNames)
	}
	for _, tool := range tools {
		wantDestructive := tool.Name == "haft_write" || tool.Name == "haft_change" || tool.Name == "haft_check"
		if tool.Annotations["readOnlyHint"] != false || tool.Annotations["destructiveHint"] != wantDestructive || tool.Annotations["idempotentHint"] != false || tool.Annotations["openWorldHint"] != false {
			t.Fatalf("%s has misleading effect annotations: %+v", tool.Name, tool.Annotations)
		}
	}

	noteID := "note-20260925-a1b2c3d4"
	body := strings.Repeat("T01A detail: ξ \" / \\ ; retained data.\n", 550)
	carrier := "---\nkind: note\nid: " + noteID + "\ntitle: Protocol extension fixture\nabout: domain:T01A.Protocol\nx-task-extension: {nested: [alpha, beta], transparent: true}\n---\n" + body
	write := app.Request{Format: delivery.Format, Operation: "remember", RequestID: "t01a-note-once", Carrier: carrier}
	invalid := []struct {
		tool string
		args any
		want string
	}{
		{"haft_read", write, "unknown_tool_or_operation"},
		{"haft_write", map[string]any{"format": delivery.Format, "operation": "remember", "action": "erase", "carrier": carrier}, "unsupported_action"},
		{"haft_write", map[string]any{"format": delivery.Format, "operation": "remember", "carrier": carrier, "query": "not a write field"}, "unsupported_field"},
		{"haft_write", map[string]any{"format": delivery.Format, "operation": "remember", "carrier": carrier, "unknown_control": true}, "unknown_field"},
	}
	longControl := strings.Repeat("unknown_control", 900)
	invalid = append(invalid, struct {
		tool string
		args any
		want string
	}{"haft_write", map[string]any{"format": delivery.Format, "operation": "remember", longControl: true}, "unknown_field"})
	for _, attempt := range invalid {
		invalidResult, rpcErr := client.call(t, attempt.tool, attempt.args)
		if rpcErr != nil || !invalidResult.IsError || invalidResult.Kind != attempt.want {
			t.Fatalf("invalid call %s: want structured %s, got %+v / %+v", attempt.tool, attempt.want, invalidResult, rpcErr)
		}
	}
	memoryPath := filepath.Join(root, ".haft")
	if _, err := os.Stat(memoryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid calls created project memory: %v", err)
	}
	written := client.mustCall(t, "haft_write", write)
	if written.Kind != "written" || written.IsError {
		t.Fatalf("corrected write: %+v", written)
	}
	replayed := client.mustCall(t, "haft_write", write)
	if replayed.Kind != "replayed" || replayed.IsError || replayed.Basis["memory_generation"] != written.Basis["memory_generation"] {
		t.Fatalf("identical request_id changed receipt basis: %+v / %+v", written, replayed)
	}
	firstData, firstOK := written.Data.(map[string]any)
	replayData, replayOK := replayed.Data.(map[string]any)
	if !firstOK || !replayOK || firstData["transaction_id"] == "" || firstData["transaction_id"] != replayData["transaction_id"] {
		t.Fatalf("identical request_id did not preserve transaction receipt: %+v / %+v", written.Data, replayed.Data)
	}

	recallRequest := app.Request{Format: delivery.Format, Operation: "recall", Ref: noteID, Limit: 1}
	recalled := client.mustCall(t, "haft_read", recallRequest)
	if recalled.Kind != "found" || recalled.IsError {
		t.Fatalf("exact note lookup: %+v", recalled)
	}
	ctx := context.Background()
	direct := service.Call(ctx, recallRequest)
	got, err := json.Marshal(recalled)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(direct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("MCP and direct haft.api/2 read diverged")
	}
	parts := t01aParts(t, client, recalled)
	bodyPart, ok := parts["body"]
	if !ok {
		t.Fatalf("body missing from returned part directory: %v", parts)
	}
	readBody, pages := t01aTextPart(t, client, recalled.Delivery.ReadTool, bodyPart)
	bodyBytes := []byte(body)
	if !bytes.Equal(readBody, bodyBytes) || pages < 2 {
		t.Fatalf("body continuation lost bytes or never paged: %d bytes, %d pages", len(readBody), pages)
	}
	recordPart, ok := parts["record"]
	if !ok {
		t.Fatal("record part missing")
	}
	record := t01aRead(t, client, recalled.Delivery.ReadTool, recordPart.Request)
	recordJSON, err := json.Marshal(record.Data)
	if err != nil {
		t.Fatal(err)
	}
	extensionName := []byte("x-task-extension")
	receiptName := []byte("payload_digest")
	if !bytes.Contains(recordJSON, extensionName) || !bytes.Contains(recordJSON, receiptName) {
		t.Fatal("unknown authored extension or write receipt disappeared from exact record")
	}
	recordData, ok := record.Data.(map[string]any)
	if !ok {
		t.Fatalf("record detail is not an object: %+v", record.Data)
	}
	receipt, ok := recordData["write_receipt"].(map[string]any)
	if !ok || receipt["request_id"] != write.RequestID {
		t.Fatalf("saved receipt lost request_id: %+v", recordData["write_receipt"])
	}
	payloadDigest, ok := receipt["payload_digest"].(string)
	digestPrefix := "sha256:"
	if !ok || len(payloadDigest) != len(digestPrefix)+64 || !strings.HasPrefix(payloadDigest, digestPrefix) {
		t.Fatalf("saved payload digest is not exact SHA-256: %+v", receipt)
	}

	changeList := client.mustCall(t, "haft_change", app.Request{Format: delivery.Format, Operation: "change", Action: "list"})
	if changeList.IsError {
		t.Fatalf("change/list was not executable: %+v", changeList)
	}
	check := client.mustCall(t, "haft_check", app.Request{Format: delivery.Format, Operation: "check"})
	if check.IsError || check.Operation != "check" {
		t.Fatalf("check tool was not executable: %+v", check)
	}
	source := client.mustCall(t, "haft_fpf", app.Request{Format: delivery.Format, Operation: "fpf", Action: "status"})
	if source.Kind != "available" || source.IsError {
		t.Fatalf("FPF status was not executable against pinned fixture: %+v", source)
	}
	badRead := client.mustCall(t, "haft_read", app.Request{Format: delivery.Format, Operation: "read", Ref: "missing:part", View: "detail", Part: "body"})
	if !badRead.IsError {
		t.Fatalf("missing read was not an error result: %+v", badRead)
	}

	legacy := t01aStartClient(t, service, ProfileLegacy)
	legacyTools := legacy.list(t)
	if len(legacyTools) != 1 || legacyTools[0].Name != "haft" {
		t.Fatalf("legacy profile advertised %v", legacyTools)
	}
	legacySchema := legacyTools[0].InputSchema
	legacyProperties, ok := legacySchema["properties"].(map[string]any)
	if !ok || legacySchema["additionalProperties"] != false {
		t.Fatalf("legacy schema is not closed: %+v", legacySchema)
	}
	actionSchema, ok := legacyProperties["action"].(map[string]any)
	if !ok || !strings.Contains(actionSchema["description"].(string), "remember") || !strings.Contains(actionSchema["description"].(string), "change") {
		t.Fatalf("legacy action description lost the catalog: %+v", actionSchema)
	}
	legacyAnnotations := legacyTools[0].Annotations
	if legacyAnnotations["readOnlyHint"] != false || legacyAnnotations["destructiveHint"] != true || legacyAnnotations["idempotentHint"] != false || legacyAnnotations["openWorldHint"] != false {
		t.Fatalf("legacy annotations are misleading: %+v", legacyAnnotations)
	}
	legacyInvalid, rpcErr := legacy.call(t, "haft", map[string]any{"format": delivery.Format, "operation": "remember", "action": "erase", "carrier": carrier})
	if rpcErr != nil || legacyInvalid.Kind != "unsupported_action" || !legacyInvalid.IsError {
		t.Fatalf("legacy invalid action was not rejected before effect: %+v / %+v", legacyInvalid, rpcErr)
	}
	legacyDefaultReplay := legacy.mustCall(t, "haft", write)
	if legacyDefaultReplay.Kind != "replayed" || legacyDefaultReplay.IsError || legacyDefaultReplay.Basis["memory_generation"] != written.Basis["memory_generation"] {
		t.Fatalf("legacy retry of default-profile write changed publication: %+v / %+v", written, legacyDefaultReplay)
	}
	legacyDefaultData, ok := legacyDefaultReplay.Data.(map[string]any)
	if !ok || legacyDefaultData["transaction_id"] != firstData["transaction_id"] {
		t.Fatalf("legacy retry of default-profile write changed transaction: %+v / %+v", written.Data, legacyDefaultReplay.Data)
	}
	legacyDefaultRead := legacy.mustCall(t, "haft", recallRequest)
	legacyDefaultParts := t01aParts(t, legacy, legacyDefaultRead)
	legacyDefaultRecord := t01aRead(t, legacy, legacyDefaultRead.Delivery.ReadTool, legacyDefaultParts["record"].Request)
	legacyDefaultRecordData, ok := legacyDefaultRecord.Data.(map[string]any)
	if !ok {
		t.Fatalf("default-profile record unavailable through legacy: %+v", legacyDefaultRecord)
	}
	legacyDefaultReceipt, ok := legacyDefaultRecordData["write_receipt"].(map[string]any)
	if !ok || legacyDefaultReceipt["payload_digest"] != payloadDigest {
		t.Fatalf("legacy retry changed default-profile payload digest: %+v", legacyDefaultRecordData["write_receipt"])
	}
	legacyNoteID := "note-20260925-a1b2c3d5"
	legacyCarrier := strings.Replace(carrier, noteID, legacyNoteID, 1)
	legacyWrite := app.Request{Format: delivery.Format, Operation: "remember", RequestID: "t01a-legacy-once", Carrier: legacyCarrier}
	legacyWritten := legacy.mustCall(t, "haft", legacyWrite)
	if legacyWritten.Kind != "written" || legacyWritten.IsError {
		t.Fatalf("legacy write failed: %+v", legacyWritten)
	}
	legacyNoteRead := app.Request{Format: delivery.Format, Operation: "recall", Ref: legacyNoteID}
	legacySaved := legacy.mustCall(t, "haft", legacyNoteRead)
	legacySavedParts := t01aParts(t, legacy, legacySaved)
	legacySavedRecord := t01aRead(t, legacy, legacySaved.Delivery.ReadTool, legacySavedParts["record"].Request)
	legacyRecordData, ok := legacySavedRecord.Data.(map[string]any)
	if !ok {
		t.Fatalf("legacy record missing: %+v", legacySavedRecord)
	}
	legacyReceipt, ok := legacyRecordData["write_receipt"].(map[string]any)
	if !ok || legacyReceipt["request_id"] != legacyWrite.RequestID {
		t.Fatalf("legacy receipt missing: %+v", legacyRecordData["write_receipt"])
	}
	legacyDigest := legacyReceipt["payload_digest"]
	legacyReplayed := legacy.mustCall(t, "haft", legacyWrite)
	if legacyReplayed.Kind != "replayed" || legacyReplayed.IsError || legacyReplayed.Basis["memory_generation"] != legacyWritten.Basis["memory_generation"] {
		t.Fatalf("legacy request_id retry changed publication: %+v / %+v", legacyWritten, legacyReplayed)
	}
	legacyFirstData, firstOK := legacyWritten.Data.(map[string]any)
	legacyReplayData, replayOK := legacyReplayed.Data.(map[string]any)
	if !firstOK || !replayOK || legacyFirstData["transaction_id"] == "" || legacyFirstData["transaction_id"] != legacyReplayData["transaction_id"] {
		t.Fatalf("legacy request_id retry changed transaction: %+v / %+v", legacyWritten.Data, legacyReplayed.Data)
	}
	legacySavedAfter := legacy.mustCall(t, "haft", legacyNoteRead)
	legacyAfterParts := t01aParts(t, legacy, legacySavedAfter)
	legacyAfterRecord := t01aRead(t, legacy, legacySavedAfter.Delivery.ReadTool, legacyAfterParts["record"].Request)
	legacyAfterData, ok := legacyAfterRecord.Data.(map[string]any)
	if !ok {
		t.Fatalf("legacy record disappeared after retry: %+v", legacyAfterRecord)
	}
	legacyAfterReceipt, ok := legacyAfterData["write_receipt"].(map[string]any)
	if !ok || legacyAfterReceipt["payload_digest"] != legacyDigest {
		t.Fatalf("legacy retry changed saved payload digest: %+v / %+v", legacyReceipt, legacyAfterData["write_receipt"])
	}
	defaultLegacyReplay := client.mustCall(t, "haft_write", legacyWrite)
	if defaultLegacyReplay.Kind != "replayed" || defaultLegacyReplay.IsError || defaultLegacyReplay.Basis["memory_generation"] != legacyWritten.Basis["memory_generation"] {
		t.Fatalf("default retry of legacy-profile write changed publication: %+v / %+v", legacyWritten, defaultLegacyReplay)
	}
	defaultLegacyData, ok := defaultLegacyReplay.Data.(map[string]any)
	if !ok || defaultLegacyData["transaction_id"] != legacyFirstData["transaction_id"] {
		t.Fatalf("default retry of legacy-profile write changed transaction: %+v / %+v", legacyWritten.Data, defaultLegacyReplay.Data)
	}
	defaultLegacyRead := client.mustCall(t, "haft_read", legacyNoteRead)
	defaultLegacyParts := t01aParts(t, client, defaultLegacyRead)
	defaultLegacyRecord := t01aRead(t, client, defaultLegacyRead.Delivery.ReadTool, defaultLegacyParts["record"].Request)
	defaultLegacyRecordData, ok := defaultLegacyRecord.Data.(map[string]any)
	if !ok {
		t.Fatalf("legacy-profile record unavailable through default: %+v", defaultLegacyRecord)
	}
	defaultLegacyReceipt, ok := defaultLegacyRecordData["write_receipt"].(map[string]any)
	if !ok || defaultLegacyReceipt["payload_digest"] != legacyDigest {
		t.Fatalf("default retry changed legacy-profile payload digest: %+v", defaultLegacyRecordData["write_receipt"])
	}
	legacyRecall := legacy.mustCall(t, "haft", recallRequest)
	if legacyRecall.Kind != "found" || legacyRecall.Delivery.ReadTool != "haft" {
		t.Fatalf("legacy lookup did not advertise its own read tool: %+v", legacyRecall)
	}
	legacyParts := t01aParts(t, legacy, legacyRecall)
	legacyBody, legacyPages := t01aTextPart(t, legacy, legacyRecall.Delivery.ReadTool, legacyParts["body"])
	if !bytes.Equal(legacyBody, bodyBytes) || legacyPages < 2 {
		t.Fatalf("legacy continuation failed: %d bytes, %d pages", len(legacyBody), legacyPages)
	}
}

func TestT01AFrozenB1ReceiptAndBytesSurviveTaskTools(t *testing.T) {
	root, basis, request, frozen := t01aFrozenB1(t)
	manifestPath := filepath.ToSlash(filepath.Join(".haft", "transactions", basis.TransactionID, "manifest.json"))
	var manifest struct {
		TransactionID string `json:"transaction_id"`
		RequestID     string `json:"request_id"`
		PayloadDigest string `json:"payload_digest"`
	}
	if err := json.Unmarshal(frozen[manifestPath], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.TransactionID != basis.TransactionID || manifest.RequestID != basis.RequestID || manifest.PayloadDigest != basis.PayloadDigest {
		t.Fatalf("B1 manifest and pinned basis disagree: %+v", manifest)
	}
	commitPath := filepath.ToSlash(filepath.Join(".haft", "transactions", basis.TransactionID, "commit.json"))
	var commit struct {
		ManifestDigest string `json:"manifest_digest"`
	}
	if err := json.Unmarshal(frozen[commitPath], &commit); err != nil {
		t.Fatal(err)
	}
	if commit.ManifestDigest != carrier.Digest(frozen[manifestPath]) {
		t.Fatalf("B1 commit marker differs from frozen manifest: %+v", commit)
	}
	service := app.Service{Root: root}
	defaultClient := t01aStartClient(t, service, ProfileDefault)
	defaultClient.list(t)
	t01aAssertB1Read(t, defaultClient, "haft_read", basis, frozen)
	defaultReplay := defaultClient.mustCall(t, "haft_write", request)
	if defaultReplay.Kind != "replayed" || defaultReplay.IsError || defaultReplay.Basis["memory_generation"] != basis.Generation {
		t.Fatalf("default task tool did not replay B1 receipt: %+v", defaultReplay)
	}
	defaultData, ok := defaultReplay.Data.(map[string]any)
	if !ok || defaultData["transaction_id"] != basis.TransactionID {
		t.Fatalf("default task tool changed B1 transaction: %+v", defaultReplay.Data)
	}
	legacyClient := t01aStartClient(t, service, ProfileLegacy)
	legacyClient.list(t)
	t01aAssertB1Read(t, legacyClient, "haft", basis, frozen)
	legacyReplay := legacyClient.mustCall(t, "haft", request)
	if legacyReplay.Kind != "replayed" || legacyReplay.IsError || legacyReplay.Basis["memory_generation"] != basis.Generation {
		t.Fatalf("legacy profile did not replay B1 receipt: %+v", legacyReplay)
	}
	legacyData, ok := legacyReplay.Data.(map[string]any)
	if !ok || legacyData["transaction_id"] != basis.TransactionID {
		t.Fatalf("legacy profile changed B1 transaction: %+v", legacyReplay.Data)
	}
	t01aAssertB1Read(t, defaultClient, "haft_read", basis, frozen)
	t01aAssertB1Read(t, legacyClient, "haft", basis, frozen)
	for relative, want := range frozen {
		path := filepath.Join(root, relative)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("B1 durable bytes were rewritten: %s", relative)
		}
	}
	noteRoot := filepath.Join(root, ".haft", "notes")
	notes, err := os.ReadDir(noteRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("B1 retry created duplicate notes: %d", len(notes))
	}
}
