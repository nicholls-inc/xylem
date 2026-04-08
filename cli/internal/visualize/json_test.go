package visualize

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRenderJSON(t *testing.T) {
	g := fixtureGraph()
	var buf bytes.Buffer
	if err := RenderJSON(g, &buf); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}

	var round Graph
	if err := json.Unmarshal(buf.Bytes(), &round); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}

	if len(round.Sources) != len(g.Sources) {
		t.Errorf("source count mismatch: got %d want %d", len(round.Sources), len(g.Sources))
	}
	if len(round.Workflows) != len(g.Workflows) {
		t.Errorf("workflow count mismatch: got %d want %d", len(round.Workflows), len(g.Workflows))
	}
	if len(round.MissingWorkflows) != 1 || round.MissingWorkflows[0] != "ghost" {
		t.Errorf("missing workflow round-trip failed: %+v", round.MissingWorkflows)
	}

	// Gate should round-trip as a pointer to the correct type.
	if round.Workflows[0].Phases[1].Gate == nil {
		t.Fatalf("expected gate on fix phase")
	}
	if round.Workflows[0].Phases[1].Gate.Type != "command" {
		t.Errorf("gate type mismatch: %s", round.Workflows[0].Phases[1].Gate.Type)
	}
}
