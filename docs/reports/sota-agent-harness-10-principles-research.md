# Research Report: Relevance of the “10 Principles” Synthesis to the SoTA Agent Harness Spec

## Executive summary

Yes — the `10 Principles` material is useful for evolving `docs/design/sota-agent-harness-spec.md`, but primarily as a **research synthesis and prioritization layer**, not as an authority to adopt wholesale.

The strongest value comes from claims that are supported by primary research and align with the current spec’s existing themes: **context as a compiled view, verification over self-evaluation, containment over prompt defense, intrinsic observability, disciplined tool contracts, and entropy management**. In practice, the reviewed material most strongly supports:

- making context-management guidance more explicit with **prompt slotting rules**
- strengthening **typed handoffs** for long-running and multi-agent work
- defining a concrete **observability schema** for runs and evidence packets
- requiring a **single-agent baseline** before multi-agent escalation
- separating **generation, deterministic prechecks, evaluator review, and human gates**
- formalizing **institutional memory primitives** with rationale, examples, and freshness metadata
- adding a **hardening lifecycle** from prompt prototype to deterministic tool

The site is less reliable where it presents exact thresholds, ratios, or stylized heuristics without enough primary-source detail. Those claims are still useful as hypotheses, but they should be treated as **tentative synthesis requiring revalidation**, not as spec-ready facts.

Bottom line: maintainers should use the strongest research-backed parts to make the existing spec **more concrete in areas it already identifies as under-specified**, rather than reframing the spec around the website’s rhetoric.

## What was reviewed

This report synthesizes:

1. The current SoTA Agent Harness spec baseline in `docs/design/sota-agent-harness-spec.md`
2. The `10 Principles` overview page and research index as a synthesis layer
3. The ranked relevant principle pages identified during prior research:
   - Context Hygiene (P2)
   - Observability (P7)
   - Token Economy (P9)
   - Disposable Blueprint (P4)
   - Institutional Memory (P5)
   - Specialized Review (P6)
   - Strategic Human Gate (P8)
   - Living Documentation (P3)
   - Hardening (P1)
4. The supplied external synthesis of primary-source-backed findings, especially:
   - Liu et al., *Lost in the Middle* (2024)
   - Wu et al., *On the Emergence of Position Bias in Transformers* (2025)
   - Cemri et al., *Why Do Multi-Agent LLM Systems Fail?* / MAST (2025)
   - Hong et al., *MetaGPT* (2023/24)
   - *Captain Agent* (2024)
   - Zamfirescu-Pereira et al., *Why Johnny Can’t Prompt* (CHI 2023)
   - Anthropic, *Building Effective Agents*

**Scope note:** the website is clearly a synthesis layer. It usefully aggregates sources, but it also mixes direct findings with stronger editorial framing. This report therefore distinguishes between findings that appear well grounded in primary work and claims that still need canonical-source revalidation.

## Findings with confidence labels

### High confidence: primary-paper-backed or strongly supported by primary guidance

#### 1. Context placement matters enough that the spec should move from “small context” to **structured context slotting**

**Confidence:** High
**Evidence basis:** Liu et al. (2024); Wu et al. (2025)

Two strong findings converge here:

- *Lost in the Middle* shows that relevant information placed in the middle of long context performs worse than information placed near the beginning or end.
- Wu et al. indicates that this middle-position weakness is architectural, not something that prompting style alone is likely to remove.

**Why this matters for the spec**

The current spec already emphasizes context compilation and signal density. The research supports making that guidance more operational by specifying **where** different classes of information should go, not just **how much** context to provide.

**Spec implication**

The spec should define slotting rules such as:

- stable policy, constraints, and invariants first
- active task state, next actions, and blocking questions last
- bulky references, examples, and archival material retrieved on demand rather than stuffed inline

---

#### 2. Multi-agent systems fail in recurring, classifiable ways; orchestration needs stronger failure-aware contracts

**Confidence:** High
**Evidence basis:** Cemri et al. / MAST (2025); Hong et al. (MetaGPT); Captain Agent (2024)

The MAST work is especially relevant because it studies a large number of traces across multiple frameworks and identifies **14 failure modes** spanning system design, inter-agent misalignment, and task verification. MetaGPT adds evidence that structured intermediate artifacts and SOP-like decomposition improve coherence. Captain Agent adds evidence that **adaptive** team composition can outperform fixed team structures.

**Why this matters for the spec**

This directly supports one of the baseline spec’s identified augmentation areas: multi-agent coordination and conflict resolution. The evidence suggests multi-agent failure should be treated as a **systems design concern**, not mainly as a prompt-writing concern.

**Spec implication**

If multi-agent execution is used, the harness should require:

- typed handoffs
- explicit assumptions and unresolved risks
- bounded role responsibilities
- evidence-bearing outputs
- conflict detection and escalation rules

---

#### 3. “Start simple” should remain a first-class harness rule, not just a stylistic preference

**Confidence:** High
**Evidence basis:** Anthropic, *Building Effective Agents*; indirect reinforcement from MAST

Anthropic’s guidance strongly supports:

- starting with the simplest workflow that works
- preferring workflows before agents
- separating evaluators from generators
- adding orchestration complexity only when measured gains justify it

This is also consistent with the MAST finding that more coordination surfaces create more failure modes.

**Why this matters for the spec**

The current spec already notes harness simplification/deprecation as a high-value augmentation area. This research supports turning that into a more explicit rule.

**Spec implication**

The spec should require a **single-agent baseline** before multi-agent escalation and should justify escalation with measured gains in success, latency, cost, or risk reduction.

---

#### 4. Instruction quality improves when policy memory includes rationale and examples, not just prohibitions

**Confidence:** High
**Evidence basis:** Zamfirescu-Pereira et al. (CHI 2023)

*Why Johnny Can’t Prompt* supports a practical lesson: non-experts struggle to steer models reliably, and prompt efficacy improves when instructions include **examples and rationale**. Negative-only instructions are weak.

**Why this matters for the spec**

This is directly relevant to agent-readable policy, institutional memory, and reusable harness instructions. It strengthens the case that policy artifacts should not just say “never do X”; they should also say what to do instead and why.

**Spec implication**

Persistent memory or policy artifacts should include:

- positive instruction
- negative constraint
- rationale
- example
- owner / review date

---

#### 5. Verification should stay externalized and separated from generation

**Confidence:** High
**Evidence basis:** Anthropic guidance; MetaGPT intermediate verification patterns

The reviewed material reinforces a core current-spec principle: **verification over self-evaluation**. The important extension is not just “have an evaluator,” but to make the order of evaluation more explicit:

1. deterministic prechecks
2. structured evidence capture
3. evaluator-model review where needed
4. human review only at high-blast-radius boundaries

**Why this matters for the spec**

This supports more concrete guidance around verification boundaries, run evidence, and when human review is warranted.

---

### Medium confidence: useful synthesis, but partly editorial or incompletely validated from canonical sources

#### 6. Living documentation is a harness concern, not just a docs concern

**Confidence:** Medium
**Evidence basis:** site synthesis; consistent with current spec themes, but not fully established here from a single canonical paper

The site’s framing that stale docs become poisoned context is directionally compelling and aligns with the current spec’s emphasis on the repository as system of record and entropy management.

**Why this matters for the spec**

Even if the framing is somewhat editorial, the design consequence is still useful: agent-critical docs should have owners, freshness checks, and stale-doc alerts.

---

#### 7. Hardening recurring prompt procedures into deterministic tools is a useful lifecycle model

**Confidence:** Medium
**Evidence basis:** site synthesis; consistent with established engineering practice and spec themes

The site’s “hardening principle” is persuasive as an engineering pattern: use prompt-based workflows for exploration, then replace recurring, safety-critical, or determinism-sensitive steps with tools or checks.

**Why this matters for the spec**

This aligns strongly with the existing spec’s containment, verification, and architectural-enforcement themes. The main caution is that the report reviewed here does not independently validate every quantitative claim used to motivate that framing.

---

### Lower confidence: useful as hypotheses, not as spec facts yet

#### 8. Exact token-efficiency thresholds and cost ratios

**Confidence:** Low to Medium
**Evidence basis:** website synthesis requiring canonical revalidation

Claims such as exact token-cost ratios or a “45% threshold” for when multi-agent escalation becomes worthwhile may be directionally useful, but they should be treated as **unverified heuristics** until maintainers recover the canonical source and preserve the original methods and caveats.

**Spec implication**

Do not encode these numeric thresholds into the spec without revalidation.

---

#### 9. Prompt-format heuristics presented as universal rules

**Confidence:** Low to Medium
**Evidence basis:** website synthesis requiring canonical revalidation

Examples include:

- exact persona-length thresholds such as “under 50 tokens”
- general claims that flattery degrades quality
- vocabulary-routing claims
- precise prompt-format variance numbers attributed on the site

These may be true in some settings, but the reviewed material does not justify adopting them as stable, model-independent harness rules.

**Spec implication**

These are better treated as **evaluation hypotheses** for local experiments, not normative requirements.

## Implications for the current SoTA Agent Harness spec

Relative to the current spec, the most valuable external additions are:

1. **Context compilation specifics**
   The current spec says “compiled view”; the research supports defining slotting strategy, retrieval boundaries, and placement discipline explicitly.

2. **Multi-agent coordination and conflict resolution**
   This is the clearest gap with strong external support. The MAST findings justify typed handoffs, role contracts, failure taxonomies, and escalation rules.

3. **Agent-readable observability patterns**
   The observability story should be made more concrete at the run-artifact level so that downstream agents and reviewers can consume it.

4. **Harness simplification and deprecation**
   Anthropic’s simple-first guidance supports an explicit rule that orchestration complexity must justify itself against a baseline and be retired when it stops earning its cost.

5. **Institutional memory design**
   The spec can move from “persist memory” to “persist policy in a format that is actually steerable for future agents.”

6. **Hardening boundaries**
   The spec can more clearly describe when successful fuzzy workflows should graduate into deterministic tools, validators, or contracts.

## Recommended additions / changes to the spec

1. **Add prompt slotting rules**
   - stable policy/constraints first
   - active task state and next actions last
   - bulky references on demand via retrieval or tools

2. **Require typed handoff artifacts for multi-step or multi-agent work**
   Minimum fields:
   - plan
   - assumptions
   - outputs produced
   - evidence references
   - unresolved risks
   - timestamps / hashes / provenance

3. **Require a single-agent baseline before multi-agent escalation**
   Multi-agent orchestration should be justified with measured gains, not preference.

4. **Support adaptive team composition instead of fixed reviewer panels**
   If specialization is needed, prefer dynamic role assignment based on task type and evidence over always-on static panels.

5. **Strengthen generation/evaluation separation**
   Recommend deterministic prechecks before evaluator-model review, plus explicit evidence packets for downstream review.

6. **Add a run observability schema**
   Include at minimum:
   - tool logs
   - model and model version
   - prompt hash
   - artifact hashes
   - review latency
   - verification results
   - handoff provenance

7. **Add institutional memory primitives**
   Persistent policy records should include:
   - positive instruction
   - negative constraint
   - rationale
   - example
   - owner
   - review date

8. **Add strategic human gates only at high-blast-radius boundaries**
   Human approval should be triggered by risk and evidence thresholds, not sprinkled across normal flow.

9. **Add living-doc freshness checks**
   Require ownership and stale-doc alerts for agent-critical documentation and operational guidance.

10. **Add a hardening lifecycle**
    Define a path from:
    - exploratory prompt/procedure
    - to stabilized workflow
    - to deterministic tool / contract / check
    when behavior becomes recurring and safety-critical.

## Open questions / items needing revalidation

1. Which website claims can be traced back to canonical primary sources with intact methods and caveats?
2. Are any exact token or threshold claims reproducible in xylem-like harness workloads?
3. How much of the site’s prompt-format guidance survives across model families and versions?
4. What is the smallest useful typed-handoff schema for xylem’s target workflows?
5. Which MAST failure modes map most directly to the orchestration patterns assumed by the current spec?
6. What observability fields are minimally sufficient for both human debugging and agent self-correction?
7. Where should the spec draw the boundary between policy memory and living documentation to avoid duplication?

## Final assessment

The `10 Principles` material is **worth using as an input to spec evolution**, especially for sharpening the spec around:

- context slotting
- typed handoffs
- multi-agent failure handling
- run-level observability
- institutional memory
- hardening boundaries

Its best use is as a **research map and synthesis layer**. Maintainers should adopt the parts grounded in primary work now, and treat the more stylized threshold-based or heuristic claims as items for local validation before they become normative spec language.
