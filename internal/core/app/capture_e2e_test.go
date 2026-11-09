package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/m0n0x41d/haft/internal/core/carrier"
	"github.com/m0n0x41d/haft/internal/core/check"
	"github.com/m0n0x41d/haft/internal/core/delivery"
)

const captureMarkerTest = `package answer
import (
	"os"
	"strings"
	"testing"
)
func TestAnswer(t *testing.T) {
	if err := os.MkdirAll(".haft/.runtime", 0700); err != nil { t.Fatal(err) }
	f, err := os.OpenFile(".haft/.runtime/capture-runs.marker", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil { t.Fatal(err) }
	if _, err = f.Write([]byte("run\n")); err != nil { t.Fatal(err) }
	if err = f.Close(); err != nil { t.Fatal(err) }
	if Answer() != 1 { t.Fatal("answer must be one") }
	t.Log(strings.Repeat("fixture-output-", 1600))
}
`

func captureFixture(t *testing.T, testSource string) (Service, Request) {
	t.Helper()
	s, q := runnerRegressionFixture(t, map[string]string{
		"answer.go":      "package answer\nfunc Answer() int { return 1 }\n",
		"answer_test.go": testSource,
	})
	q.Action = "capture"
	q.RequestID = "capture-fixture"
	q.Capture = &CaptureOptions{TimeoutMillis: 30000, MaxOutputBytes: 1 << 20}
	return s, q
}

func captureRef(t *testing.T, r delivery.Response) string {
	t.Helper()
	if r.Delivery.Catalog == nil || !strings.HasPrefix(r.Delivery.Catalog.Ref, "result:") {
		t.Fatalf("capture omitted executable result continuation: %+v", r.Delivery)
	}
	return r.Delivery.Catalog.Ref
}

func capturePart(t *testing.T, s Service, r delivery.Response, name string) delivery.Request {
	t.Helper()
	for _, p := range r.Delivery.Available {
		if p.Name == name {
			return p.Request
		}
	}
	if r.Delivery.Catalog == nil {
		t.Fatalf("capture result has no part catalog: %+v", r)
	}
	q := *r.Delivery.Catalog
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		page := public(t, s, nextApp(q))
		var data struct {
			Parts []delivery.Descriptor `json:"parts"`
		}
		if err := json.Unmarshal(rawJSON(page.Data), &data); err != nil {
			t.Fatal(err)
		}
		for _, p := range data.Parts {
			if p.Name == name {
				return p.Request
			}
		}
		if page.Delivery.Next == nil {
			t.Fatalf("capture result omitted part %s", name)
		}
		q = *page.Delivery.Next
	}
	t.Fatalf("capture part catalog did not terminate for %s", name)
	return delivery.Request{}
}

func captureRuns(t *testing.T, root string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ".haft", ".runtime", "capture-runs.marker"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(raw, []byte("run\n"))
}

// The caller issues a single explicit operation. Prepare, result reads and a
// replay of the same request cannot become another project-test execution.
func TestCaptureActualPassRetryReadAndRetain(t *testing.T) {
	if testing.Short() {
		t.Skip("actual isolated Go test process")
	}
	s, q := captureFixture(t, captureMarkerTest)
	prepared := q
	prepared.Action = "prepare"
	prepared.RequestID = ""
	prepared.Capture = nil
	preparedResult := public(t, s, prepared)
	if preparedResult.Kind != "prepared" || captureRuns(t, s.Root) != 0 {
		t.Fatalf("prepare started a project test: kind=%s runs=%d", preparedResult.Kind, captureRuns(t, s.Root))
	}
	var preparedEnvironment map[string]string
	preparedEnvironmentPart := deliveredPart(t, s, preparedResult, "run_environment")
	if err := json.Unmarshal(collectPart(t, s, preparedEnvironmentPart), &preparedEnvironment); err != nil {
		t.Fatal(err)
	}

	// Discard the first reply to model a lost transport response. A retry is
	// required to recover the same result instead of rerunning the test.
	first := public(t, s, q)
	if first.Kind != check.Passed || captureRuns(t, s.Root) != 1 {
		t.Fatalf("actual pass did not execute exactly once: %+v runs=%d", first, captureRuns(t, s.Root))
	}
	firstRef := captureRef(t, first)
	retry := public(t, s, q)
	if retry.Kind != check.Passed || captureRef(t, retry) != firstRef || captureRuns(t, s.Root) != 1 {
		t.Fatalf("lost-reply retry reran or changed the capture: %+v runs=%d", retry, captureRuns(t, s.Root))
	}
	replaySummary := object(retry.Data)
	if replaySummary["execution_replay"] != true || replaySummary["recorded_post_run_basis"] != "same" || replaySummary["current_basis_assessment"] != "same" || replaySummary["current_basis"] != "same" {
		t.Fatalf("same-basis lost reply is not an explicit non-executing replay: %+v", replaySummary)
	}

	stdoutQ := capturePart(t, s, retry, "stdout")
	stdout := collectPart(t, s, stdoutQ)
	if len(stdout) <= delivery.Budget || !bytes.Contains(stdout, []byte("fixture-output-")) {
		t.Fatalf("large process output was not readable in chunks: %d bytes", len(stdout))
	}
	processQ := capturePart(t, s, retry, "process")
	var process struct {
		Command        []string          `json:"command"`
		CWD            string            `json:"cwd"`
		Environment    map[string]string `json:"environment"`
		ExitCode       *int              `json:"exit_code"`
		StdoutBytes    int               `json:"stdout_bytes"`
		StdoutDigest   string            `json:"stdout_digest"`
		OutputExceeded bool              `json:"output_exceeded"`
	}
	if err := json.Unmarshal(collectPart(t, s, processQ), &process); err != nil {
		t.Fatal(err)
	}
	if process.StdoutBytes != len(stdout) || process.StdoutDigest != carrier.Digest(stdout) || process.OutputExceeded {
		t.Fatalf("full-byte process accounting differs from read continuation: %+v", process)
	}
	if len(process.Command) < 6 || process.Command[0] != "go" || process.Command[1] != "test" || process.CWD != s.Root || process.ExitCode == nil || *process.ExitCode != 0 || process.Environment["GOWORK"] != "off" || process.Environment["GOTOOLCHAIN"] != "local" {
		t.Fatalf("bounded process did not disclose its fixed command/cwd/env and exit: %+v", process)
	}
	if !sameJSON(preparedEnvironment, process.Environment) || process.Environment["GOPROXY"] != "off" || process.Environment["GOSUMDB"] != "off" {
		t.Fatalf("prepare and actual capture used different pinned Go environments: prepared=%v process=%v", preparedEnvironment, process.Environment)
	}
	obsQ := capturePart(t, s, retry, "observation")
	var observed check.Observation
	if err := json.Unmarshal(collectPart(t, s, obsQ), &observed); err != nil {
		t.Fatal(err)
	}
	if observed.Status != check.Passed || observed.Input.Observed.Selector.Test != "TestAnswer" {
		t.Fatalf("real selected pass absent from stored observation: %+v", observed)
	}

	// The caller provides its own carrier and names a server-side part. It never
	// copies process bytes into the authored request.
	keep := Request{Operation: "remember", RequestID: "retain-capture-output", Carrier: "---\nkind: note\ntitle: Captured project test output\nabout: domain:Capture.Answer\n---\nThe bounded fixture test completed.\n", Retain: []Retention{{Ref: firstRef, Part: "stdout"}}}
	if saved := public(t, s, keep); saved.Kind != "written" {
		t.Fatalf("server-side retain failed: %+v", saved)
	}
	var savedID string
	for _, doc := range readView(t, s).Documents {
		if doc.Record.Title == "Captured project test output" {
			savedID = doc.Record.ID
		}
	}
	if savedID == "" {
		t.Fatal("retained note was not published")
	}
	restarted := Service{Root: s.Root}
	if got := public(t, restarted, nextApp(stdoutQ)); got.Kind != check.Passed || captureRuns(t, s.Root) != 1 {
		t.Fatalf("continuation after restart reexecuted capture: %+v", got)
	}
	if err := os.RemoveAll(filepath.Join(s.Root, ".haft", ".cache", "disclosure")); err != nil {
		t.Fatal(err)
	}
	if got := public(t, restarted, nextApp(stdoutQ)); got.Kind != "expired" || captureRuns(t, s.Root) != 1 {
		t.Fatalf("lost disposable capture silently reran: %+v", got)
	}
	if got := public(t, restarted, q); got.Kind != "capture_expired" || captureRuns(t, s.Root) != 1 || len(got.Diagnostics) == 0 || !strings.Contains(got.Diagnostics[0].Message, "new request_id") {
		t.Fatalf("retry after cache loss lacked a fresh-capture action: %+v", got)
	}
	retained := public(t, restarted, Request{Operation: "recall", Ref: savedID})
	if !bytes.Equal(collectPart(t, restarted, deliveredPart(t, restarted, retained, "report_1_content")), stdout) {
		t.Fatal("durably retained output differs from the original process bytes")
	}
	if err := os.RemoveAll(filepath.Join(s.Root, ".haft", ".cache")); err != nil {
		t.Fatal(err)
	}
	if got := public(t, restarted, q); got.Kind != "capture_expired" || captureRuns(t, s.Root) != 1 {
		t.Fatalf("whole-cache loss allowed the original request_id to rerun: %+v runs=%d", got, captureRuns(t, s.Root))
	}
}

func TestCaptureActualOutcomesAndNonexecution(t *testing.T) {
	if testing.Short() {
		t.Skip("actual isolated Go test processes")
	}
	cases := []struct {
		name, source, want string
		withoutMatcher     bool
	}{
		{"assertion-failure", "package answer\nimport \"testing\"\nfunc TestAnswer(t *testing.T) { if Answer()!=2 { t.Fatal(\"answer must be one\") } }\n", check.AssertionFailure, false},
		{"missing-matcher", "package answer\nimport \"testing\"\nfunc TestAnswer(t *testing.T) { if Answer()!=2 { t.Fatal(\"answer must be one\") } }\n", check.Unattributable, true},
		{"skipped", "package answer\nimport \"testing\"\nfunc TestAnswer(t *testing.T) { t.Skip(\"fixture unavailable\") }\n", check.Skipped, false},
		{"zero-tests", "package answer\nimport (\"os\"; \"testing\")\nfunc TestMain(m *testing.M) { os.Exit(0) }\nfunc TestAnswer(t *testing.T) { if Answer()!=1 { t.Fatal(\"answer must be one\") } }\n", check.NotRun, false},
		{"build-failure", "package answer\nimport \"testing\"\nfunc TestAnswer(t *testing.T) { var broken MissingType; _ = broken }\n", check.EnvironmentFailure, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, q := captureFixture(t, tc.source)
			if tc.withoutMatcher {
				q.FailureContract = ""
				q.FailurePattern = ""
			}
			got := public(t, s, q)
			if got.Kind != tc.want {
				t.Fatalf("actual process classification %s, want %s: %+v", got.Kind, tc.want, got)
			}
			if _, err := json.Marshal(got); err != nil {
				t.Fatal(err)
			}
			_ = collectPart(t, s, capturePart(t, s, got, "observation"))
		})
	}

	s, q := captureFixture(t, captureMarkerTest)
	q.CheckRef = "test:answer_test.go::TestOther"
	wrong := public(t, s, q)
	if wrong.Kind != "check_basis_unresolved" || captureRuns(t, s.Root) != 0 {
		t.Fatalf("undeclared selector executed or passed: %+v", wrong)
	}
	read := public(t, s, Request{Operation: "read", Ref: "result:sha256:" + strings.Repeat("0", 64)})
	if read.Kind == check.Passed || captureRuns(t, s.Root) != 0 {
		t.Fatalf("read started a project test: %+v", read)
	}
}

func TestCaptureTimeoutAndOutputLimitDoNotPass(t *testing.T) {
	if testing.Short() {
		t.Skip("actual isolated Go test processes")
	}
	t.Run("timeout", func(t *testing.T) {
		source := "package answer\nimport (\"testing\"; \"time\")\nfunc TestAnswer(t *testing.T) { time.Sleep(10*time.Second) }\n"
		s, q := captureFixture(t, source)
		q.Capture.TimeoutMillis = 1500
		start := time.Now()
		got := public(t, s, q)
		if got.Kind != check.EnvironmentFailure || time.Since(start) > 8*time.Second {
			t.Fatalf("bounded timeout gave %s after %s: %+v", got.Kind, time.Since(start), got)
		}
	})
	t.Run("incomplete-output", func(t *testing.T) {
		s, q := captureFixture(t, captureMarkerTest)
		q.Capture.MaxOutputBytes = 512
		got := public(t, s, q)
		if got.Kind == check.Passed || captureRuns(t, s.Root) != 1 {
			t.Fatalf("truncated runner output promoted to pass: %+v", got)
		}
		var process struct {
			OutputExceeded bool   `json:"output_exceeded"`
			StdoutBytes    int64  `json:"stdout_bytes"`
			StdoutDigest   string `json:"stdout_digest"`
		}
		if err := json.Unmarshal(collectPart(t, s, capturePart(t, s, got, "process")), &process); err != nil {
			t.Fatal(err)
		}
		stdout := collectPart(t, s, capturePart(t, s, got, "stdout"))
		if !process.OutputExceeded || process.StdoutBytes <= int64(len(stdout)) || process.StdoutDigest == carrier.Digest(stdout) {
			t.Fatalf("truncated prefix was presented as complete output: %+v retained=%d", process, len(stdout))
		}
	})
}

func TestCaptureChangedBasisCannotReuseOldPass(t *testing.T) {
	if testing.Short() {
		t.Skip("actual isolated Go test processes")
	}
	s, q := captureFixture(t, captureMarkerTest)
	first := public(t, s, q)
	if first.Kind != check.Passed {
		t.Fatalf("baseline fixture failed: %+v", first)
	}
	var old check.Observation
	if err := json.Unmarshal(collectPart(t, s, capturePart(t, s, first, "observation")), &old); err != nil {
		t.Fatal(err)
	}
	runnerRegressionWrite(t, s.Root, "answer.go", "package answer\nfunc Answer() int { return 2 }\n")
	got := s.Execute(context.Background(), Request{Format: Format, Operation: "check", Action: "observe", Observation: &old.Input})
	if got.Kind != check.Passed || got.Data.(map[string]any)["current_basis"] != "changed" {
		t.Fatalf("old captured pass presented as current: %+v", got)
	}
	next := q
	next.RequestID = "capture-after-basis-change"
	fresh := public(t, s, next)
	if fresh.Kind != check.AssertionFailure || captureRef(t, fresh) == captureRef(t, first) {
		t.Fatalf("changed code reused historical passing capture: %+v", fresh)
	}
}

func TestCaptureCorruptResultRequiresNewExecutionDecision(t *testing.T) {
	if testing.Short() {
		t.Skip("actual isolated Go test process")
	}
	s, q := captureFixture(t, captureMarkerTest)
	first := public(t, s, q)
	if first.Kind != check.Passed || captureRuns(t, s.Root) != 1 {
		t.Fatalf("fixture did not run once: %+v", first)
	}
	ref := captureRef(t, first)
	cachePath := filepath.Join(s.Root, ".haft", ".cache", "disclosure", strings.TrimPrefix(ref, "result:sha256:")+".json")
	if err := os.WriteFile(cachePath, []byte("corrupt cached capture"), 0600); err != nil {
		t.Fatal(err)
	}
	retry := public(t, Service{Root: s.Root}, q)
	if retry.Kind != "capture_corrupt" || captureRuns(t, s.Root) != 1 || len(retry.Diagnostics) == 0 || !strings.Contains(retry.Diagnostics[0].Message, "new request_id") {
		t.Fatalf("corrupt capture retried a process or hid recovery action: %+v", retry)
	}
}

func TestCaptureRefusesCodeChangedByToolchainProbeBeforeClaim(t *testing.T) {
	if testing.Short() {
		t.Skip("actual isolated Go toolchain and test processes")
	}
	s, q := captureFixture(t, captureMarkerTest)
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	writeGoVersionShim(t, shimDir, realGo)
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+originalPath)
	changed := public(t, s, q)
	if changed.Kind != "check_basis_changed" || captureRuns(t, s.Root) != 0 {
		t.Fatalf("probe mutation reached the project test or capture claim: %+v runs=%d", changed, captureRuns(t, s.Root))
	}
	raw, err := os.ReadFile(filepath.Join(s.Root, "answer.go"))
	if err != nil || !bytes.Contains(raw, []byte("return 2")) {
		t.Fatalf("toolchain shim did not exercise the source race: %v %q", err, raw)
	}

	// An early refusal leaves no execution claim. Restore the exact source and
	// use the same request ID; the operation must now execute once normally.
	t.Setenv("PATH", originalPath)
	runnerRegressionWrite(t, s.Root, "answer.go", "package answer\nfunc Answer() int { return 1 }\n")
	recovered := public(t, s, q)
	if recovered.Kind != check.Passed || captureRuns(t, s.Root) != 1 {
		t.Fatalf("probe-time refusal consumed the request ID or ran the test: %+v runs=%d", recovered, captureRuns(t, s.Root))
	}
}

// Go's -json path normally emits UTF-8 JSON. This boundary case exercises the
// delivery and retention path for arbitrary process bytes without claiming a
// second real runner observation.
func TestCaptureNonUTF8OutputRemainsExactAcrossReadAndRetain(t *testing.T) {
	s, q := captureFixture(t, captureMarkerTest)
	q.Format = Format
	q.forDelivery = true
	raw := []byte{0xff, 0x00, 'A', 0xfe, '\n'}
	observation := check.Observation{
		Status: check.Unattributable,
		Input: check.ObservationInput{Observed: check.Run{
			Stdout: raw,
			Stderr: raw,
		}},
	}
	result := Result{
		Format: Format, Operation: "check", Kind: check.Unattributable,
		Data: map[string]any{"observation": observation, "process": CaptureProcess{
			StdoutBytes: int64(len(raw)), StderrBytes: int64(len(raw)),
			StdoutDigest: carrier.Digest(raw), StderrDigest: carrier.Digest(raw),
			StdoutDrainComplete: true, StderrDrainComplete: true,
		}},
		Diagnostics: []carrier.Diagnostic{}, Basis: map[string]string{}, Coverage: "complete", Limits: []string{},
	}
	created := s.deliver(context.Background(), q, result, "summary", "", "")
	if created.Kind != check.Unattributable || captureRuns(t, s.Root) != 0 {
		t.Fatalf("synthetic delivery unexpectedly executed a test: %+v", created)
	}
	stdoutQ := capturePart(t, s, created, "stdout")
	stderrQ := capturePart(t, s, created, "stderr")
	for _, part := range []delivery.Request{stdoutQ, stderrQ} {
		if !bytes.Equal(collectPart(t, s, part), raw) {
			t.Fatalf("non-UTF-8 process bytes changed in %s read", part.Part)
		}
	}
	partMedia := ""
	for _, part := range created.Delivery.Available {
		if part.Name == "stdout" {
			partMedia = part.Media
		}
	}
	if partMedia == "" {
		d, err := s.loadTransient(context.Background(), captureRef(t, created), false, "")
		if err != nil {
			t.Fatal(err)
		}
		p, err := d.Member("stdout")
		if err != nil {
			t.Fatal(err)
		}
		partMedia = p.Media
	}
	if partMedia != "binary" {
		t.Fatalf("invalid UTF-8 presented as %q instead of binary", partMedia)
	}

	keep := Request{Operation: "remember", RequestID: "retain-binary-capture", Carrier: "---\nkind: note\ntitle: Retained binary capture\nabout: domain:Capture.Answer\n---\nThe raw process bytes are retained for inspection.\n", Retain: []Retention{{Ref: captureRef(t, created), Part: "stdout"}}}
	if saved := public(t, s, keep); saved.Kind != "written" {
		t.Fatalf("binary output was not retainable: %+v", saved)
	}
	var savedID string
	for _, doc := range readView(t, s).Documents {
		if doc.Record.Title == "Retained binary capture" {
			savedID = doc.Record.ID
		}
	}
	if savedID == "" {
		t.Fatal("retained binary capture was not published")
	}
	if err := os.RemoveAll(filepath.Join(s.Root, ".haft", ".cache", "disclosure")); err != nil {
		t.Fatal(err)
	}
	restarted := Service{Root: s.Root}
	retained := public(t, restarted, Request{Operation: "recall", Ref: savedID})
	content := deliveredPart(t, restarted, retained, "report_1_content")
	if !bytes.Equal(collectPart(t, restarted, content), raw) {
		t.Fatal("retained binary output changed after transient result loss")
	}
}
