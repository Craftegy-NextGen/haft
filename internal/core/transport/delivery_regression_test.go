package transport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/m0n0x41d/haft/internal/core/app"
	"github.com/m0n0x41d/haft/internal/core/carrier"
	"github.com/m0n0x41d/haft/internal/core/delivery"
	"github.com/m0n0x41d/haft/internal/testpath"
)

// Measure the actual serialized MCP result, including both representations and
// escaping. Every call also checks text-only/structuredContent/error parity.
func deliveryCall(t *testing.T, c *client, q app.Request) delivery.Response {
	t.Helper()
	q.Format = delivery.Format
	fmt.Fprint(c.in, call(2, q))
	line, err := c.out.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  any             `json:"error"`
	}
	if err := json.Unmarshal(line, &rpc); err != nil || rpc.Error != nil {
		t.Fatalf("RPC: %s (%v)", line, err)
	}
	if len(rpc.Result) > delivery.Budget {
		t.Fatalf("serialized MCP result: %d > %d", len(rpc.Result), delivery.Budget)
	}
	var wire struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Structured delivery.Response `json:"structuredContent"`
		IsError    bool              `json:"isError"`
	}
	if err := json.Unmarshal(rpc.Result, &wire); err != nil {
		t.Fatal(err)
	}
	var text delivery.Response
	if len(wire.Content) != 1 {
		t.Fatal("missing text parity")
	}
	if err := json.Unmarshal([]byte(wire.Content[0].Text), &text); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wire.Structured, text) || wire.IsError != text.IsError {
		t.Fatal("MCP parity")
	}
	return wire.Structured
}
func readDelivery(t *testing.T, c *client, q delivery.Request) delivery.Response {
	t.Helper()
	raw, _ := json.Marshal(q)
	var request app.Request
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	return deliveryCall(t, c, request)
}
func deliveryParts(t *testing.T, c *client, r delivery.Response) map[string]delivery.Descriptor {
	t.Helper()
	parts := map[string]delivery.Descriptor{}
	for q := r.Delivery.Catalog; q != nil; {
		page := readDelivery(t, c, *q)
		if page.Delivery.Encoding != "part_directory" {
			t.Fatalf("directory: %+v", page)
		}
		var data struct {
			Parts []delivery.Descriptor `json:"parts"`
		}
		raw, _ := json.Marshal(page.Data)
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		for _, p := range data.Parts {
			parts[p.Name] = p
		}
		q = page.Delivery.Next
	}
	return parts
}
func deliveryBytes(t *testing.T, c *client, q delivery.Request, kind, encoding string, isError bool) []byte {
	t.Helper()
	var full []byte
	digest := q.ExpectedDigest
	for {
		page := readDelivery(t, c, q)
		state := page.Delivery
		if page.Kind != kind || page.IsError != isError || state.Encoding != encoding {
			t.Fatalf("chunk outcome: %+v", page)
		}
		if state.Offset != len(full) || state.Digest != digest {
			t.Fatal("chunk basis/offset changed")
		}
		data := page.Data.(map[string]any)
		var chunk []byte
		if encoding == "utf-8" {
			chunk = []byte(data["text"].(string))
			if !utf8.Valid(chunk) {
				t.Fatal("split UTF-8 code point")
			}
		} else {
			var err error
			chunk, err = base64.StdEncoding.DecodeString(data["bytes_base64"].(string))
			if err != nil {
				t.Fatal(err)
			}
		}
		if len(chunk) != state.ReturnedBytes {
			t.Fatal("incorrect chunk length")
		}
		full = append(full, chunk...)
		if state.Next == nil {
			if !state.Complete || len(full) != state.TotalBytes || carrier.Digest(full) != digest {
				t.Fatal("incomplete or lossy member")
			}
			return full
		}
		if state.Complete || len(chunk) == 0 {
			t.Fatal("non-progressing continuation")
		}
		q = *state.Next
	}
}

func TestMCPCodeCaptureMemberContinuations(t *testing.T) {
	root := t.TempDir()
	fixture := map[string]string{
		"go.mod":           "module example.com/er01-greeting\n\ngo 1.25.0\n",
		"greeting.go":      "package greeting\n\nfunc Greeting(name string) string {\n\treturn \"Hello, \" + name + \" !\"\n}\n",
		"greeting_test.go": "package greeting\n\nimport \"testing\"\n\nfunc TestGreetingExact(t *testing.T) {\n\tgot := Greeting(\"Ada\")\n\twant := \"Hello, Ada!\"\n\tif got != want {\n\t\tt.Fatalf(\"greeting mismatch: got %q want %q\", got, want)\n\t}\n}\n",
	}
	for name, body := range fixture {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 20; i++ {
		path := filepath.Join(root, fmt.Sprintf("docs/guide-%02d.md", i))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Repeat("Generated project guidance 🌱\\\"\n", 40)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := newClient(t, app.Service{Root: root})
	context := deliveryCall(t, c, app.Request{Operation: "context", Query: "file:greeting.go", CaptureCode: true})
	if context.Kind != "results" || context.IsError {
		t.Fatal("context capture unavailable", context.Kind)
	}
	parts := deliveryParts(t, c, context)
	capture, ok := parts["code_capture"]
	if !ok {
		t.Fatal("context omitted code_capture route")
	}
	q := capture.Request
	keys := []string{}
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		page := readDelivery(t, c, q)
		if page.Kind != "results" || page.IsError || page.Delivery.Encoding != "json_members" || page.Delivery.Offset != len(keys) {
			t.Fatalf("code_capture member continuation failed: %+v", page)
		}
		var body struct {
			Members []struct {
				Key      string              `json:"key"`
				Complete bool                `json:"complete"`
				Read     delivery.Descriptor `json:"read"`
			} `json:"members"`
		}
		raw, err := json.Marshal(page.Data)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &body); err != nil || len(body.Members) == 0 {
			t.Fatalf("empty code_capture page: %v", err)
		}
		for _, member := range body.Members {
			keys = append(keys, member.Key)
			if member.Key != "files" {
				continue
			}
			if member.Complete {
				t.Fatal("large files member incorrectly declared complete")
			}
			request := member.Read.Request
			request.View = "bytes"
			files := deliveryBytes(t, c, request, "results", "base64", false)
			if !bytes.Contains(files, []byte("greeting.go")) || member.Read.Digest != carrier.Digest(files) {
				t.Fatal("files child route lost exact captured bytes")
			}
		}
		if page.Delivery.Next == nil {
			break
		}
		bad := *page.Delivery.Next
		bad.Limit++
		if refused := readDelivery(t, c, bad); refused.Kind != "cursor_parameters_changed" || !refused.IsError {
			t.Fatal("edited cursor accepted", refused.Kind)
		}
		q = *page.Delivery.Next
	}
	if !slices.Equal(keys, []string{"basis", "complete", "config", "files", "format"}) {
		t.Fatalf("code_capture members lost order or data: %v", keys)
	}
}

func TestMCPCodeCaptureEscapedFilenameRoutes(t *testing.T) {
	root := t.TempDir()
	escapedName := testpath.Name(strings.Repeat("\x01", 101) + ".go")
	fixture := map[string]string{
		"go.mod":           "module example.com/er01r2\n\ngo 1.25.0\n",
		"greeting.go":      "package greeting\n\nfunc Greeting() string { return \"Hello!\" }\n",
		"greeting_test.go": "package greeting\n\nimport \"testing\"\n\nfunc TestGreeting(t *testing.T) { if Greeting() != \"Hello!\" { t.Fatal(\"mismatch\") } }\n",
		escapedName:        "package greeting\n\n// " + strings.Repeat("captured bytes ", 800) + "\n",
	}
	for name, body := range fixture {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := newClient(t, app.Service{Root: root})
	context := deliveryCall(t, c, app.Request{Operation: "context", Query: "file:greeting.go", CaptureCode: true})
	if context.Kind != "results" || context.IsError {
		t.Fatal("context capture unavailable", context.Kind)
	}
	capture, ok := deliveryParts(t, c, context)["code_capture"]
	if !ok {
		t.Fatal("context omitted code_capture route")
	}
	var files delivery.Descriptor
	seenTop := []string{}
	for q := &capture.Request; q != nil; {
		page := readDelivery(t, c, *q)
		if page.Kind != "results" || page.IsError || page.Delivery.Encoding != "json_members" || page.Delivery.Offset != len(seenTop) {
			t.Fatalf("code_capture member page unavailable: %+v", page)
		}
		var body struct {
			Members []struct {
				Key  string              `json:"key"`
				Read delivery.Descriptor `json:"read"`
			} `json:"members"`
		}
		raw, err := json.Marshal(page.Data)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &body); err != nil || len(body.Members) == 0 {
			t.Fatal("empty or unreadable code_capture page", err)
		}
		for _, member := range body.Members {
			seenTop = append(seenTop, member.Key)
			if member.Key == "files" {
				files = member.Read
			}
		}
		q = page.Delivery.Next
	}
	if files.Name == "" || !slices.Equal(seenTop, []string{"basis", "complete", "config", "files", "format"}) {
		t.Fatal("top-level code_capture routes changed", seenTop)
	}
	seenFiles := map[string]bool{}
	count := 0
	for q := &files.Request; q != nil; {
		page := readDelivery(t, c, *q)
		if page.Kind != "results" || page.IsError || page.Delivery.Encoding != "json_members" || page.Delivery.Offset != count {
			t.Fatalf("files member page unavailable: %+v", page)
		}
		var body struct {
			Members []struct {
				Key         string              `json:"key"`
				KeyComplete bool                `json:"key_complete"`
				KeyRequest  *delivery.Request   `json:"key_request"`
				Read        delivery.Descriptor `json:"read"`
			} `json:"members"`
		}
		raw, err := json.Marshal(page.Data)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &body); err != nil || len(body.Members) == 0 {
			t.Fatal("empty or unreadable files page", err)
		}
		for _, member := range body.Members {
			name := member.Key
			if !member.KeyComplete {
				if member.KeyRequest == nil {
					t.Fatal("shortened filename has no exact key route")
				}
				keyRequest := *member.KeyRequest
				keyRequest.View = "bytes"
				name = string(deliveryBytes(t, c, keyRequest, "results", "base64", false))
			}
			if seenFiles[name] || !strings.HasPrefix(name, member.Key) {
				t.Fatal("filename lost, duplicated or excerpt mislabeled", name)
			}
			valueRequest := member.Read.Request
			valueRequest.View = "bytes"
			encoded := deliveryBytes(t, c, valueRequest, "results", "base64", false)
			content, err := base64.StdEncoding.DecodeString(string(encoded))
			if err != nil || string(content) != fixture[name] {
				t.Fatal("captured source bytes changed", name, err)
			}
			seenFiles[name] = true
			count++
		}
		q = page.Delivery.Next
	}
	for name := range fixture {
		if !seenFiles[name] {
			t.Fatal("captured filename missing", name)
		}
	}
}

func TestMCPDirectorySelectionAndImmutableReads(t *testing.T) {
	root := t.TempDir()
	service := app.Service{Root: root}
	c := newClient(t, service)
	created := deliveryCall(t, c, app.Request{Operation: "remember", RequestID: "r9-spec", Carrier: "---\nkind: spec\ntitle: Directory selection\nslug: r9\nabout: domain:Edge\nreceiving_use: Delivery regression\nclaims:\n- id: rule\n  kind: definition\n  text: Keep this exact claim\n  future_extension: {number: 9007199254740993}\n---\nExact body.\n"})
	if created.Kind != "written" {
		t.Fatal(created)
	}
	summary := deliveryCall(t, c, app.Request{Operation: "recall", Ref: "spec:r9#rule", Limit: 1})
	parts := deliveryParts(t, c, summary)
	first := readDelivery(t, c, *summary.Delivery.Catalog)
	if first.Delivery.Next == nil {
		t.Fatal("missing directory continuation")
	}
	oldNext := *first.Delivery.Next
	originals := map[string][]byte{}
	for _, name := range []string{"claim", "carrier", "snapshot"} {
		q := parts[name].Request
		if q.ExpectedGeneration != "" {
			t.Fatalf("immutable %s is generation-gated", name)
		}
		q.View = "bytes"
		originals[name] = deliveryBytes(t, c, q, "found", "base64", false)
	}
	header, _ := json.Marshal(map[string]any{"kind": "note", "title": "Actual backlink mutation", "about": "domain:Edge", "links": []any{map[string]any{"kind": "relies_on", "target": parts["claim"].Request.Ref, "reason": "Selected relation changed"}}})
	note := deliveryCall(t, c, app.Request{Operation: "remember", RequestID: "r9-note", Carrier: "---\n" + string(header) + "\n---\nUses the exact claim.\n"})
	if note.Kind != "written" {
		t.Fatal(note)
	}
	// An unchanged cursor must never splice a second generation into this list.
	for _, q := range []delivery.Request{oldNext, *summary.Delivery.Catalog, parts["backlinks"].Request} {
		stale := readDelivery(t, c, q)
		if stale.Kind != "stale" || !stale.IsError {
			t.Fatalf("old %s request: want stale, got %s", q.Part, stale.Kind)
		}
	}
	if oldNext.ExpectedGeneration != first.Basis["memory_generation"] {
		t.Fatal("directory continuation omitted its captured generation")
	}
	current := deliveryCall(t, c, app.Request{Operation: "recall", Ref: "spec:r9#rule", Limit: 1})
	now := deliveryParts(t, c, current)
	if now["backlinks"].Digest == parts["backlinks"].Digest {
		t.Fatal("test did not change selected backlinks")
	}
	unrelated := deliveryCall(t, c, app.Request{Operation: "remember", RequestID: "r9-unrelated", Carrier: "---\nkind: note\ntitle: Unrelated memory\nabout: domain:Other\n---\nUnrelated write.\n"})
	if unrelated.Kind != "written" {
		t.Fatal(unrelated)
	}
	if err := os.RemoveAll(filepath.Join(root, ".haft", ".cache", "disclosure")); err != nil {
		t.Fatal(err)
	}
	restarted := newClient(t, app.Service{Root: root})
	for name, want := range originals {
		q := parts[name].Request
		q.View = "bytes"
		if got := deliveryBytes(t, restarted, q, "found", "base64", false); !bytes.Equal(got, want) {
			t.Fatalf("immutable %s changed after generation/cache/restart", name)
		}
	}
}
