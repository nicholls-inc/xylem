package visualize

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// RenderStateMachine writes the graph as a Mermaid stateDiagram-v2 modelled
// as two coupled state machines:
//
//  1. Issue labels — states are sorted sets of GitHub issue/PR labels, and
//     transitions are workflow runs fired by github/github-pr sources. The
//     workflow's per-task status_labels become outbound transitions to
//     single-label states, so feedback loops (workflow writes a label that
//     is also a trigger label somewhere else) appear as cycles in the diagram.
//
//  2. PR lifecycle — states are synthetic PR events (review_submitted,
//     checks_failed, commented, merged) with any author allow/deny list
//     embedded in the display label. The same running(W) → status_label
//     fan-out applies.
//
// Label gates (workflow phases with gate.type: label wait_for: X) emit
// cross-machine edges so an operator can see when one workflow pauses for a
// label that another source uses as a trigger — the handoffs that make the
// whole system an actual state machine.
//
// Missing workflows are styled as dashed terminal states; orphan workflow
// files render as isolated running(W) nodes with no incoming edge, so gaps
// and dead code are visually obvious before any audit rule runs.
//
// Output is plain Mermaid stateDiagram-v2 text, no new dependencies.
func RenderStateMachine(g *Graph, w io.Writer) error {
	sm := newStateMachineBuilder(g)
	return sm.render(w)
}

// stateMachineBuilder accumulates the set of states and transitions so each
// label / workflow / event only produces a single state definition even when
// referenced from many triggers.
type stateMachineBuilder struct {
	g *Graph

	// issue block
	issueLabelStates map[string]string // id → display label
	issueRunStates   map[string]string // id → display label (workflows reachable from issue triggers)

	// PR block
	prEventStates map[string]string // id → display label
	prRunStates   map[string]string // id → display label (workflows reachable from PR triggers)

	// Terminal running-state outlines for missing/orphan workflows.
	missingRunStates map[string]string // id → display (workflows referenced but file missing)
	orphanRunStates  map[string]string // id → display (workflow files not referenced)

	// Status-label targets: running(W) --label--> single-label issue state.
	// Kept separately so we can create the target state in the issue block
	// even if no trigger happens to fire on that label.
	statusLabelStates map[string]string // id → display

	// Transitions inside the issue block (by (from,to,label) to dedupe).
	issueTransitions []smTransition
	// Transitions inside the PR block.
	prTransitions []smTransition
	// Cross-machine transitions: label gate handoffs.
	crossTransitions []smTransition
	// Entry transitions: [*] --> stateID, with a label.
	entryTransitions []smTransition
}

type smTransition struct {
	From  string
	To    string
	Label string
}

func newStateMachineBuilder(g *Graph) *stateMachineBuilder {
	return &stateMachineBuilder{
		g:                 g,
		issueLabelStates:  map[string]string{},
		issueRunStates:    map[string]string{},
		prEventStates:     map[string]string{},
		prRunStates:       map[string]string{},
		missingRunStates:  map[string]string{},
		orphanRunStates:   map[string]string{},
		statusLabelStates: map[string]string{},
	}
}

func (sm *stateMachineBuilder) render(w io.Writer) error {
	sm.walk()

	var b strings.Builder
	b.WriteString("stateDiagram-v2\n")
	b.WriteString("  %% xylem trigger → workflow state machine\n")
	b.WriteString("  %% Issue-label states are sorted sets (AND semantics); PR-lifecycle states are synthetic events.\n\n")

	// classDef equivalent in stateDiagram-v2: we define visual classes and
	// apply them to workflow states via `class id className`. Mermaid 10+
	// supports classDef inside stateDiagram.
	b.WriteString("  classDef workflow fill:#ede9fe,stroke:#6d28d9,color:#2e1065\n")
	b.WriteString("  classDef label fill:#ecfdf5,stroke:#059669,color:#064e3b\n")
	b.WriteString("  classDef prevent fill:#dbeafe,stroke:#1d4ed8,color:#0b1f4d\n")
	b.WriteString("  classDef missing fill:#fee2e2,stroke:#b91c1c,color:#450a0a,stroke-dasharray:4 2\n")
	b.WriteString("  classDef orphan fill:#fef3c7,stroke:#b45309,color:#451a03,stroke-dasharray:4 2\n\n")

	sm.renderIssueBlock(&b)
	b.WriteString("\n")
	sm.renderPRBlock(&b)

	// Missing & orphan workflows outside the nested blocks so it's visually
	// obvious that they have no normal trigger path.
	if len(sm.missingRunStates) > 0 || len(sm.orphanRunStates) > 0 {
		b.WriteString("\n  %% Workflows with no trigger path (missing file or orphan)\n")
		for _, id := range sortedKeys(sm.missingRunStates) {
			fmt.Fprintf(&b, "  state %q as %s\n", sm.missingRunStates[id], id)
		}
		for _, id := range sortedKeys(sm.orphanRunStates) {
			fmt.Fprintf(&b, "  state %q as %s\n", sm.orphanRunStates[id], id)
		}
	}

	// Cross-machine label gate handoffs.
	if len(sm.crossTransitions) > 0 {
		b.WriteString("\n  %% Label gate handoffs across state machines\n")
		for _, t := range sm.crossTransitions {
			writeTransition(&b, "  ", t)
		}
	}

	// Class assignments (Mermaid requires these at the end of a stateDiagram
	// after all state definitions).
	sm.renderClassAssignments(&b)

	_, err := io.WriteString(w, b.String())
	return err
}

// walk traverses the graph once, populating every map and transition slice
// on the builder. All later rendering is purely a sorted dump of this state
// — the builder is intentionally deterministic so repeated invocations
// produce byte-identical output.
func (sm *stateMachineBuilder) walk() {
	workflowsByName := map[string]*Workflow{}
	for i := range sm.g.Workflows {
		workflowsByName[sm.g.Workflows[i].Name] = &sm.g.Workflows[i]
	}

	// Track which workflows appear on each side of the split so we can pick
	// the right block for running(W) states. A workflow fired by both kinds
	// of source gets a running state in both blocks (with the same ID) — in
	// practice that's unusual, but rendering both avoids dangling edges.
	issueWorkflows := map[string]bool{}
	prWorkflows := map[string]bool{}

	// Pass 1: classify triggers into issue vs PR blocks and register the
	// trigger / workflow / status-label states.
	for _, src := range sm.g.Sources {
		for _, trig := range src.Triggers {
			if trig.Workflow == "" {
				continue
			}
			runID := workflowRunningID(trig.Workflow)
			runDisplay := fmt.Sprintf("running(%s)", trig.Workflow)

			switch src.Type {
			case "github", "github-pr":
				entryID, entryDisplay := issueLabelStateKey(trig.Labels)
				sm.issueLabelStates[entryID] = entryDisplay
				sm.issueRunStates[runID] = runDisplay
				issueWorkflows[trig.Workflow] = true
				sm.issueTransitions = append(sm.issueTransitions, smTransition{
					From:  entryID,
					To:    runID,
					Label: fmt.Sprintf("%s.%s", src.Name, trig.TaskName),
				})
				sm.entryTransitions = append(sm.entryTransitions, smTransition{
					From:  "[*]",
					To:    entryID,
					Label: "label set",
				})
				sm.addStatusLabelFanout(runID, trig.StatusLabels, true)

			case "github-pr-events":
				entryID, entryDisplay := prEventStateKey(trig)
				sm.prEventStates[entryID] = entryDisplay
				sm.prRunStates[runID] = runDisplay
				prWorkflows[trig.Workflow] = true
				sm.prTransitions = append(sm.prTransitions, smTransition{
					From:  entryID,
					To:    runID,
					Label: fmt.Sprintf("%s.%s", src.Name, trig.TaskName),
				})
				sm.entryTransitions = append(sm.entryTransitions, smTransition{
					From:  "[*]",
					To:    entryID,
					Label: "PR event",
				})
				sm.addStatusLabelFanout(runID, trig.StatusLabels, false)

			case "github-merge":
				entryID := "pr_merged"
				sm.prEventStates[entryID] = "pr merged"
				sm.prRunStates[runID] = runDisplay
				prWorkflows[trig.Workflow] = true
				sm.prTransitions = append(sm.prTransitions, smTransition{
					From:  entryID,
					To:    runID,
					Label: fmt.Sprintf("%s.%s", src.Name, trig.TaskName),
				})
				sm.entryTransitions = append(sm.entryTransitions, smTransition{
					From:  "[*]",
					To:    entryID,
					Label: "merge",
				})
				sm.addStatusLabelFanout(runID, trig.StatusLabels, false)

			default:
				// Unknown source type — register the workflow as an issue-side
				// running state so it still appears in the diagram.
				sm.issueRunStates[runID] = runDisplay
				issueWorkflows[trig.Workflow] = true
			}
		}
	}

	// Pass 2: missing workflows — referenced by a trigger but no YAML on disk.
	for _, name := range sm.g.MissingWorkflows {
		id := workflowRunningID(name)
		display := fmt.Sprintf("running(%s) — missing", name)
		sm.missingRunStates[id] = display
		// Remove from the normal run state sets so it renders only once, in
		// the missing section with its dashed style.
		delete(sm.issueRunStates, id)
		delete(sm.prRunStates, id)
	}

	// Pass 3: orphan workflow files — not referenced by any trigger.
	for _, name := range sm.g.OrphanedWorkflows {
		id := workflowRunningID(name)
		display := fmt.Sprintf("running(%s) — orphan", name)
		sm.orphanRunStates[id] = display
	}

	// Pass 4: label gate handoffs. A phase with gate.type == "label" pauses
	// the workflow until an external label shows up; draw that as an edge
	// from running(W) to the single-label issue state wait_for refers to.
	for _, wf := range sm.g.Workflows {
		for _, p := range wf.Phases {
			if p.Gate == nil || p.Gate.Type != "label" || p.Gate.WaitFor == "" {
				continue
			}
			fromID := workflowRunningID(wf.Name)
			toID, toDisplay := issueLabelStateKey([]string{p.Gate.WaitFor})
			sm.issueLabelStates[toID] = toDisplay
			sm.crossTransitions = append(sm.crossTransitions, smTransition{
				From:  fromID,
				To:    toID,
				Label: fmt.Sprintf("gate: wait for %s", p.Gate.WaitFor),
			})
		}
	}

	// Promote any statusLabelStates into the issue label block — status
	// labels are persistent labels on the issue/PR so they belong there.
	for id, display := range sm.statusLabelStates {
		if _, ok := sm.issueLabelStates[id]; !ok {
			sm.issueLabelStates[id] = display
		}
	}
}

// addStatusLabelFanout emits running(W) → single-label state transitions for
// every populated per-task status_label. If preferIssue is false (PR-side
// trigger) we still register the label as an issue-label state because
// status_labels semantically live on the GitHub object.
func (sm *stateMachineBuilder) addStatusLabelFanout(runID string, statusLabels map[string]string, preferIssue bool) {
	_ = preferIssue
	for _, key := range sortedKeys(statusLabels) {
		label := statusLabels[key]
		if label == "" {
			continue
		}
		stateID, display := issueLabelStateKey([]string{label})
		sm.statusLabelStates[stateID] = display
		// All status-label transitions live in the issue block (since the
		// label sits on the issue/PR, not on an ephemeral event).
		sm.issueTransitions = append(sm.issueTransitions, smTransition{
			From:  runID,
			To:    stateID,
			Label: key,
		})
	}
}

func (sm *stateMachineBuilder) renderIssueBlock(b *strings.Builder) {
	hasContent := len(sm.issueLabelStates) > 0 || len(sm.issueRunStates) > 0 || len(sm.issueTransitions) > 0
	if !hasContent {
		return
	}
	b.WriteString("  state \"Issue labels\" as issues {\n")
	for _, id := range sortedKeys(sm.issueLabelStates) {
		fmt.Fprintf(b, "    state %q as %s\n", sm.issueLabelStates[id], id)
	}
	for _, id := range sortedKeys(sm.issueRunStates) {
		fmt.Fprintf(b, "    state %q as %s\n", sm.issueRunStates[id], id)
	}
	// Entry transitions for issue states.
	seen := map[string]bool{}
	for _, t := range sm.entryTransitions {
		if _, ok := sm.issueLabelStates[t.To]; !ok {
			continue
		}
		key := t.From + "->" + t.To + ":" + t.Label
		if seen[key] {
			continue
		}
		seen[key] = true
		writeTransition(b, "    ", t)
	}
	// Trigger + status-label transitions.
	deduped := dedupeTransitions(sm.issueTransitions)
	for _, t := range deduped {
		writeTransition(b, "    ", t)
	}
	b.WriteString("  }\n")
}

func (sm *stateMachineBuilder) renderPRBlock(b *strings.Builder) {
	hasContent := len(sm.prEventStates) > 0 || len(sm.prRunStates) > 0 || len(sm.prTransitions) > 0
	if !hasContent {
		return
	}
	b.WriteString("  state \"PR lifecycle\" as pr {\n")
	for _, id := range sortedKeys(sm.prEventStates) {
		fmt.Fprintf(b, "    state %q as %s\n", sm.prEventStates[id], id)
	}
	for _, id := range sortedKeys(sm.prRunStates) {
		fmt.Fprintf(b, "    state %q as %s\n", sm.prRunStates[id], id)
	}
	seen := map[string]bool{}
	for _, t := range sm.entryTransitions {
		if _, ok := sm.prEventStates[t.To]; !ok {
			continue
		}
		key := t.From + "->" + t.To + ":" + t.Label
		if seen[key] {
			continue
		}
		seen[key] = true
		writeTransition(b, "    ", t)
	}
	deduped := dedupeTransitions(sm.prTransitions)
	for _, t := range deduped {
		writeTransition(b, "    ", t)
	}
	b.WriteString("  }\n")
}

func (sm *stateMachineBuilder) renderClassAssignments(b *strings.Builder) {
	var runIDs []string
	for id := range sm.issueRunStates {
		runIDs = append(runIDs, id)
	}
	for id := range sm.prRunStates {
		if _, dup := sm.issueRunStates[id]; dup {
			continue
		}
		runIDs = append(runIDs, id)
	}
	sort.Strings(runIDs)

	var labelIDs []string
	for id := range sm.issueLabelStates {
		labelIDs = append(labelIDs, id)
	}
	sort.Strings(labelIDs)

	var prIDs []string
	for id := range sm.prEventStates {
		prIDs = append(prIDs, id)
	}
	sort.Strings(prIDs)

	if len(runIDs) > 0 || len(labelIDs) > 0 || len(prIDs) > 0 ||
		len(sm.missingRunStates) > 0 || len(sm.orphanRunStates) > 0 {
		b.WriteString("\n")
	}
	for _, id := range runIDs {
		fmt.Fprintf(b, "  class %s workflow\n", id)
	}
	for _, id := range labelIDs {
		fmt.Fprintf(b, "  class %s label\n", id)
	}
	for _, id := range prIDs {
		fmt.Fprintf(b, "  class %s prevent\n", id)
	}
	for _, id := range sortedKeys(sm.missingRunStates) {
		fmt.Fprintf(b, "  class %s missing\n", id)
	}
	for _, id := range sortedKeys(sm.orphanRunStates) {
		fmt.Fprintf(b, "  class %s orphan\n", id)
	}
}

// issueLabelStateKey canonicalises a set of labels into a Mermaid state ID
// and a human-readable display label. Labels are sorted so {ready, bug}
// and {bug, ready} produce the same state.
func issueLabelStateKey(labels []string) (id, display string) {
	if len(labels) == 0 {
		return "issue_empty", "{}"
	}
	sorted := append([]string(nil), labels...)
	sort.Strings(sorted)
	// Deduplicate in place.
	dedup := sorted[:0]
	var last string
	for _, l := range sorted {
		if l == last {
			continue
		}
		dedup = append(dedup, l)
		last = l
	}
	display = "{" + strings.Join(dedup, ", ") + "}"
	id = "issue_" + sanitizeID(strings.Join(dedup, "__"))
	return id, display
}

// prEventStateKey builds a state key for a github-pr-events trigger. The
// author allowlist/denylist is embedded in the display label (but not the
// ID) so two tasks with different author filters still collapse to the same
// state when the event set matches — the operator sees both allow/deny
// lists side by side on the state node.
func prEventStateKey(t Trigger) (id, display string) {
	var events []string
	if t.OnReview {
		events = append(events, "review_submitted")
	}
	if t.OnChecks {
		events = append(events, "checks_failed")
	}
	if t.OnComment {
		events = append(events, "commented")
	}
	if len(t.Labels) > 0 {
		// PR-event sources can also filter by label (task.on.labels); fold
		// those into the state key so label-gated events don't collide with
		// unfiltered ones.
		labelsSorted := append([]string(nil), t.Labels...)
		sort.Strings(labelsSorted)
		events = append(events, "labels:"+strings.Join(labelsSorted, ","))
	}
	if len(events) == 0 {
		events = []string{"pr_event"}
	}
	id = "pr_" + sanitizeID(strings.Join(events, "__"))
	parts := make([]string, 0, len(events))
	for _, e := range events {
		parts = append(parts, strings.ReplaceAll(e, "_", " "))
	}
	display = strings.Join(parts, " / ")
	if len(t.AuthorAllow) > 0 {
		display += " (allow: " + strings.Join(t.AuthorAllow, ", ") + ")"
	}
	if len(t.AuthorDeny) > 0 {
		display += " (deny: " + strings.Join(t.AuthorDeny, ", ") + ")"
	}
	return id, display
}

func workflowRunningID(name string) string {
	return "run_" + sanitizeID(name)
}

func writeTransition(b *strings.Builder, indent string, t smTransition) {
	if t.Label == "" {
		fmt.Fprintf(b, "%s%s --> %s\n", indent, t.From, t.To)
		return
	}
	// stateDiagram-v2 transition label syntax: `from --> to : label`. Label
	// must not contain a raw colon (splits the label early), so escape any
	// embedded colons to avoid truncation.
	safe := strings.ReplaceAll(t.Label, ":", "\u2236")
	fmt.Fprintf(b, "%s%s --> %s : %s\n", indent, t.From, t.To, safe)
}

func dedupeTransitions(in []smTransition) []smTransition {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]smTransition, 0, len(in))
	for _, t := range in {
		key := t.From + "->" + t.To + ":" + t.Label
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
