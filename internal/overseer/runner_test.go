package overseer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewAbstentionCreatesNoFindings(t *testing.T) {
	packet, err := BuildPacket(BuildInput{
		Producer: DefaultProducer("test"),
		Subject: Subject{
			Kind:     "commit",
			Ref:      "HEAD",
			SHA:      "abc123",
			DiffHash: "sha256:diff",
		},
		RepoState: RepoState{GitRoot: ".", Branch: "main"},
		ChangedFiles: []ChangedFile{{
			Path:   "internal/cli/init.go",
			Status: "modified",
		}},
		Budget: DefaultContextBudget(),
	})
	if err != nil {
		t.Fatalf("BuildPacket returned error: %v", err)
	}

	stored := StoredRun{
		Packet: packet,
		Run:    NewDeterministicReviewRun(packet, "2026-06-09T00:00:00Z"),
	}
	input := ReviewAbstention(DefaultConfig(), stored, "codex command unavailable")
	if input.Verdict != "review_abstained" {
		t.Fatalf("verdict = %q, want review_abstained", input.Verdict)
	}
	if len(input.Findings) != 0 {
		t.Fatalf("abstention findings = %d, want 0", len(input.Findings))
	}
	if len(input.ScopeCoverage.Abstentions) != 1 {
		t.Fatalf("abstentions = %d, want 1", len(input.ScopeCoverage.Abstentions))
	}
}

func TestReviewResultJSONSchemaIsStrictForCodexStructuredOutput(t *testing.T) {
	schema := ReviewResultJSONSchema()
	if strings.Contains(schema, `"additionalProperties": true`) {
		t.Fatalf("schema contains loose nested object settings:\n%s", schema)
	}
	for _, want := range []string{
		`"reviewer": {`,
		`"additionalProperties": false`,
		`"required": ["reviewer", "verdict", "findings", "non_findings_under_scope"]`,
		`"required": ["agent"]`,
		`"non_findings_under_scope": {`,
		`"line_start": {"type": "integer"}`,
		`"required": ["path", "line_start"]`,
	} {
		if !strings.Contains(schema, want) {
			t.Fatalf("schema missing %q:\n%s", want, schema)
		}
	}
}

func TestReviewResultJSONSchemaRequiresEveryStrictObjectProperty(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(ReviewResultJSONSchema()), &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}

	assertStrictRequiredProperties(t, schema, "$")
}

func assertStrictRequiredProperties(t *testing.T, node map[string]any, path string) {
	t.Helper()

	if node["additionalProperties"] == false {
		properties, _ := node["properties"].(map[string]any)
		required := requiredSet(node["required"])
		for key := range properties {
			if !required[key] {
				t.Fatalf("%s required missing property %q", path, key)
			}
		}
	}

	if properties, _ := node["properties"].(map[string]any); len(properties) > 0 {
		for key, child := range properties {
			childMap, ok := child.(map[string]any)
			if ok {
				assertStrictRequiredProperties(t, childMap, path+".properties."+key)
			}
		}
	}

	items, ok := node["items"].(map[string]any)
	if ok {
		assertStrictRequiredProperties(t, items, path+".items")
	}
}

func requiredSet(value any) map[string]bool {
	out := map[string]bool{}
	values, ok := value.([]any)
	if !ok {
		return out
	}
	for _, item := range values {
		key, ok := item.(string)
		if ok {
			out[key] = true
		}
	}
	return out
}
