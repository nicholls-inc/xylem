# Spec for a State-of-the-Art Agent Harness

## Executive Summary

A state-of-the-art (SoTA) agent harness in 2026 is **not** just a prompt wrapper around a model. It is a full execution system that manages context, tools, memory, evaluation, observability, security, and repository legibility as first-class engineering concerns.[1][2][3][4] The strongest cross-vendor consensus is that reliable agents come from **small, high-signal context**, **clear and low-overlap tools**, **live-system verification**, **mechanical enforcement of invariants**, and **sandboxed execution with least privilege**.[2][5][6][7][8]

The best current harnesses also treat the repository as the system of record for agent-readable knowledge, use progressive disclosure rather than encyclopedic instruction files, and encode human judgment into evaluators, linters, docs, and recurring cleanup jobs.[1][2][9][10] Long-running work requires explicit state artifacts, checkpointing, compaction or resets, and disciplined handoffs between sessions or sub-agents.[3][11][12]

Recent formal-method and compiler-guided systems add an important refinement: deterministic tooling increases trust only when the checker is external to the model, the checked claim is explicit, and the trust boundary is documented. In practice that means proof assistants, compilers, schema validators, and other external checkers should be treated as layered evidence, not as blanket substitutes for integration testing or live validation.[18][20][21][22][23][24][25][26][27]

My bottom line is: **build today for observability, verification, least privilege, context compilation, and repository legibility; do not build today for speculative full autonomy.** The next generation of agentic engineering is likely to move toward more learned policies, more environment-native safety, thinner orchestration layers, and more compiler-like context systems, but those are still emerging and should be separated from today’s production baseline.[4][11][13][14][15]

---

## Scope and Method

This document separates:

1. **Validated SoTA**: practices I would recommend shipping today because they are supported by strong primary-source evidence from Anthropic, Google, Microsoft, AWS, GitHub, OWASP, NVIDIA, Apple, MCP documentation, and your existing local research synthesis.[1][2][3][4][5][6][7][8][9][10][11][13][15][16]
2. **Predictions**: forward-looking extrapolations from recent research and vendor direction. These are plausible, but they should not be confused with current consensus.[4][11][13][14][15]

I treat OpenAI’s harness-engineering claims as credible but partially indirect in this report because the original article was blocked during fetch. I therefore cite your local synthesis plus reputable secondary coverage where needed, and I weight primary sources more heavily when making normative recommendations.[1][10][17]

---

# Part I — Validated SoTA (Build This Now)

## 1. Definition

**An agent harness is the engineered execution environment around the model**: instructions, tool plane, memory/state, orchestration, evaluation, security controls, observability, and repository structure.[1][2][4] Context engineering is a subset of harness engineering: it optimizes what the model sees, while the harness governs the broader system that makes agents reliable over time.[2][16]

## 2. Design Goals

A SoTA harness SHOULD optimize for the following, in priority order:

| Goal | Why it matters |
| --- | --- |
| Reliability under long horizons | Many useful agent tasks span many turns, multiple tools, and sometimes multiple sessions. |
| Verifiability | Agents improve most on tasks with executable checks, live testing, or clear evaluation criteria. |
| Context efficiency | More context is not a free win; relevance density matters more than raw token volume. |
| Safety by architecture | Prompt injection remains an unsolved systems problem; containment must be enforced outside the model. |
| Maintainability | Harness complexity must be inspectable, observable, and reducible as models improve. |
| Legibility to agents | Repos, docs, and tools must be discoverable and mechanically validated for agent use, not just human taste. |

Supporting citations:

- Reliability under long horizons: [3][11]
- Verifiability: [5][11]
- Context efficiency: [2][4]
- Safety by architecture: [7][8]
- Maintainability: [5][11]
- Legibility to agents: [1][9][10]

## 3. Non-Goals

A SoTA harness SHOULD NOT assume:

- that a giant context window removes the need for context engineering;[2][4]
- that more tools automatically improve outcomes;[2][6]
- that agents can reliably self-grade their own output without an external evaluator or ground-truth checks;[5][11]
- that formal verification, compiler success, or any other single deterministic gate is sufficient to establish end-to-end correctness; these tools verify bounded claims and still leave specification quality, integration boundaries, and live behavior to other checks;[18][20][21][24][25][26][27]
- that prompt-only defenses are sufficient for hostile inputs or tool-bearing agents;[7][8]
- that multi-agent systems are always superior to single-agent systems.[5][16]

---

## 4. Normative Specification

The following sections use **MUST / SHOULD / MAY** language intentionally.

### 4.1 Repository and Knowledge Layer

The harness **MUST** treat the repository as the authoritative knowledge base for the agent. Critical instructions, architecture notes, execution plans, task maps, and decision records must be versioned and co-located with the codebase, because information in Slack, docs portals, or human memory is effectively invisible to the agent.[1][3][10]

The harness **MUST NOT** rely on a monolithic `AGENTS.md` or equivalent instruction file. Instead, it **MUST** use a short, stable entry-point document that acts as a map and points to deeper documents organized by topic.[1][2][9] GitHub’s guidance on successful `agents.md` files reinforces the same pattern at the persona level: specific role, commands early, concrete examples, and explicit boundaries.[9]

The knowledge base **SHOULD** include at minimum:

- bootstrap/setup instructions;
- canonical build/test/lint/run commands;
- architecture map and module ownership;
- active execution plans and technical-debt notes;
- task-specific references or product/design docs;
- examples of correct output or code style.[1][3][9][10]

### 4.2 Context Management Layer

The harness **MUST** treat context as a **compiled view** rather than a bag of concatenated strings. Durable state and per-call prompt views should be separate concepts, as in Google ADK’s session/working-context model.[4]

The harness **MUST** minimize context volume and maximize signal density. Anthropic’s guidance is explicit: the goal is the smallest sufficient set of high-signal tokens, not maximal inclusion.[2] For that reason, the harness **SHOULD** combine four techniques:

1. **Write**: persist state outside the window via notes, plans, scratchpads, or progress files;[2][3]
2. **Select**: retrieve only relevant documents, memories, or tools;[2][4]
3. **Compress**: summarize stale history and trim redundant tool output;[2][4]
4. **Isolate**: use sub-agents or bounded threads as context firewalls when tasks branch or tool chatter becomes large.[2][16]

The harness **SHOULD** support both **compaction** and **context reset with structured handoff**. Compaction is cheaper and increasingly viable with newer models, but Anthropic’s long-running-harness work shows that full resets can still be superior when context anxiety or long-range drift appears.[2][11]

### 4.3 Tool Plane

The harness **MUST** expose tools with:

- clear names;
- explicit parameters and value domains;
- documented failure modes;
- narrow, non-overlapping responsibilities;
- token-efficient outputs.[2][5][6]

The harness **SHOULD** support **MCP** as one important integration protocol, especially for cross-vendor interoperability, external systems, and centrally governed tool catalogs.[6][13]

The harness **MUST NOT** assume MCP is the universally best interface for every tool. The stronger evidence supports a narrower claim: MCP has meaningful ecosystem support and is useful where discoverability, typed schemas, remote access, and shared integration contracts matter; local or developer-owned workflows may still be better served by direct CLI tools or other thinner interfaces when they are cheaper, simpler, or more reliable in practice.[3][6][13]

The harness **SHOULD** prefer **just-in-time retrieval via tools** over preloading everything into context. Anthropic explicitly recommends lightweight references plus runtime exploration, and OpenAI’s public harness reporting emphasizes the same “map first, fetch as needed” pattern.[1][2]

The tool plane **MUST** be designed for ambiguity reduction. AWS’s guidance is particularly strong here: clarity matters more than brevity, and vague tool descriptions materially degrade selection quality.[6]

### 4.4 Memory and State Layer

The harness **MUST** separate at least three kinds of state:

- **procedural memory**: rules, instruction files, policies;[16]
- **semantic memory**: facts and durable knowledge;[4][16]
- **episodic/task memory**: what happened during this task or past similar tasks.[16]

For long-running tasks, the harness **MUST** persist structured handoff artifacts outside the conversational context. Anthropic’s validated patterns include progress files, feature lists, init scripts, and git history; Google and Microsoft formalize similar ideas via sessions/threads and structured state stores.[3][4][15]

The harness **SHOULD** use **structured artifacts rather than freeform notes** when possible. Anthropic reports that JSON feature lists are more robust than Markdown for status tracking because the model is less likely to “creatively” rewrite them.[3]

The memory layer **MUST** implement:

- per-user/per-session isolation;
- sanitization before persistence;
- retention and expiration policy;
- auditability for sensitive data exposure.[7]

### 4.5 Orchestration Layer

The harness **MUST** start simple and only add orchestration complexity when measured results justify it. Anthropic’s practical recommendation remains one of the strongest high-signal principles in the field.[5]

The harness **SHOULD** support these orchestration modes:

- single-agent loop for bounded tasks;[5]
- prompt chaining for predictable pipelines;[5]
- routing for task-type specialization;[5]
- orchestrator-workers for dynamic decomposition;[5]
- evaluator-optimizer loops for quality-sensitive work;[5][11]
- multi-agent handoff for context isolation or domain separation.[4][15][16]

However, the harness **MUST NOT** assume multi-agent is inherently better. Multi-agent systems can consume much more context and cost; they are justified when the problem is open-ended, tool-heavy, or benefits materially from context firewalls or role separation.[2][5][16]

The harness **SHOULD** separate **generator** and **evaluator** roles whenever task quality cannot be reliably measured by unit tests alone. Anthropic’s long-running-harness work shows that self-evaluation is systematically weak, while a skeptical evaluator plus explicit criteria produces materially better outcomes.[11]

### 4.6 Verification Layer

The harness **MUST** verify the artifact or live system produced by the agent, not just the code diff. This is one of the clearest consensus points across Anthropic, AWS, and GitHub-era coding agents.[3][6][10][11]

The verification layer **MUST** include:

- deterministic checks where available (tests, type-checks, linters, schema validation);[5][10]
- live-system validation for UI/API workflows (browser automation, endpoint checks, DB assertions);[3][11]
- explicit pass/fail criteria for claims of task completion;[3][11]
- evidence capture such as screenshots, logs, DOM snapshots, or trace excerpts when the result cannot be established from code alone.[10][11]

The verification layer **SHOULD** distinguish at least four evidence levels:

- **proved**: a proof checker or theorem prover accepted a formal statement about a bounded artifact;[18][20][21][22][23][24][25]
- **mechanically checked**: a compiler, type-checker, schema validator, model checker, or similar deterministic tool accepted the artifact;[20][21][26]
- **behaviorally checked**: property-based, differential, or other executable checks exercised the implementation against explicit expectations;[18][20][21][26][27]
- **observed in situ**: screenshots, traces, logs, DB state, or operator review established facts about the running system.[3][10][11]

The harness **MUST** record which category supports each important completion claim. Saying that something is “verified” without naming the checked proposition, checker, and trust boundary is too vague to be operationally useful.[18][19][20][21][24][27]

If the harness uses proof-producing tools, it **MUST** surface a small human-reviewable artifact — preconditions, postconditions, invariants, or a prose restatement of the proof goal — before elevating trust. Crosscheck and Midspiral both converge on this pattern for the same reason: the remaining hard problem is often “did we specify the right thing?” rather than “can the checker validate the proof?”.[18][24][27]

Formal verification is highest leverage when the problem is pure or mostly functional, the specification is small enough for human review, and the integration boundary is explicit. That is the recurring pattern across Dafny-style workflows, Lean-based theorem proving, Cedar’s verification-guided development, and recent proof-oriented agent systems such as Ax-Prover, Aristotle, and Leanstral.[18][20][21][22][23][25]

The harness **MUST NOT** treat proof production as a substitute for checking the boundary where verified code meets the rest of the system. Cedar’s verification-guided development pairs Lean proofs with differential random testing and property-based testing of the Rust implementation, and one local verification case study surfaced integration bugs and test-theatre failures outside the proved core. A serious harness should therefore pair formal specs with boundary checks such as differential tests, schema validation, and spec-derived property tests against the deployed language runtime.[18][20][21][27]

The harness **SHOULD** add interface or contract-consistency checks where failures commonly arise between components, such as mismatched numeric precision, enum/value conventions, auth policy semantics, or generated-vs-runtime type expectations. Broadly this aligns with established practice around schema validation and model/code equivalence; graph-level contract verification for application stacks is promising but still emerging rather than established SoTA.[19][20][21][27]

Compilation success is a useful deterministic gate, and systems like AutoBE show how to bake compiler feedback into generation loops, but compilation alone is weaker evidence than proofs, boundary checks, or live-system validation. It should be interpreted as “structurally accepted by the toolchain,” not “correct at runtime.”[26]

### 4.7 Evaluation Layer

Evaluation in this spec refers to **measuring the quality of the harness itself**: prompts, tool interfaces, routing policies, memory strategies, model choices, and orchestration behavior.[6][14]

The evaluation layer **MUST** be wired into every harness change. AWS’s guidance is especially clear: prompt changes, tool additions, model swaps, and routing changes should all trigger evaluation.[6]

The evaluation layer **MUST** include:

- regression datasets, trace-based benchmarks, or scenario suites representative of the target workload;[6][14]
- tracked outcome metrics such as task success, latency, cost, tool error rates, and failure modes;[6]
- comparison across harness variants when changing prompts, models, routing, or tool surfaces;[6][11]
- periodic calibration against human judgment for subjective or evaluator-driven tasks.[11]

### 4.8 Observability Layer

The harness **MUST** instrument model calls, tool calls, latency, error rates, token usage, verification evidence, and evaluation outcomes from day one.[6][10]

The observability layer **SHOULD** be agent-readable. Strong public harness patterns now expose logs, metrics, traces, screenshots, and browser state directly to agents so they can debug and validate autonomously.[1][10][11]

This implies the harness **SHOULD** provide:

- trace-level debugging during development;[6]
- dashboards and anomaly monitoring in production;[6][7]
- per-task or per-worktree isolated environments where the agent can boot and inspect the system;[10]
- artifact capture such as screenshots, DOM snapshots, or log excerpts for evidence-based evaluation.[10][11]

### 4.9 Security and Sandboxing Layer

The harness **MUST** assume prompt injection is not fully solved at the model level and therefore enforce trust boundaries architecturally.[7][8]

At minimum, the sandbox **MUST** enforce:

- network egress restrictions by default;[8]
- no writes outside the workspace;[8]
- no writes to agent configuration/instruction files, even inside the workspace;[8]
- least-privilege tool access and secret scope;[7][8]
- human approval for high-risk or irreversible actions;[7][10]
- audit logs for tool use and security-relevant events.[7]

The sandbox **SHOULD** isolate the entire agent-bearing environment, not just shell calls. NVIDIA is explicit that hooks, MCP startup scripts, skills, and other spawned processes must also be contained, ideally under separate users and preferably with virtualization rather than a shared kernel.[8]

For high-trust enterprise systems, the harness **SHOULD** move toward **microVM / full VM / strong virtualization** rather than shared-kernel containers alone.[8]

### 4.10 Architectural Enforcement and Entropy Management

The harness **MUST** mechanically enforce architectural invariants via CI checks, structural tests, import/dependency rules, or custom linters, rather than treating architecture as prose-only guidance.[1][10]

The enforcement target **SHOULD** be invariants, not implementations. In other words, specify the shape of correctness and safety boundaries, while allowing the agent some local freedom inside those boundaries.[1][16]

The harness **SHOULD** also include **entropy management**: recurring cleanup agents, doc gardeners, or quality bots that continuously repair drift, stale docs, and pattern sprawl. This is now a defining characteristic of frontier harnesses rather than an optional nice-to-have.[1][10]

### 4.11 Cost and Efficiency Layer

The harness **MUST** measure token, latency, and cost at the run and task level.[6][7]

The efficiency strategy **SHOULD** include:

- context compaction thresholds;[2][4]
- stable prompt prefixes to exploit caching where supported;[4]
- summarization of verbose tool outputs;[2]
- model routing or model ladders by task difficulty;[5][6]
- sub-agents as context firewalls only when they actually improve quality enough to justify the cost.[2][16]

The harness **MUST** periodically simplify itself. Anthropic’s best harness lesson is that every harness component encodes an assumption about what the model cannot do; as models improve, some scaffolding becomes dead weight and should be removed.[11]

---

## 5. Reference Architecture

```text
┌────────────────────────────────────────────────────────────────────┐
│                         Human Governance Layer                    │
│  task definition • approvals • policy • review • escalation      │
└───────────────┬───────────────────────────────────────┬────────────┘
                │                                       │
                ▼                                       ▼
┌────────────────────────────┐              ┌─────────────────────────┐
│   Knowledge / Repo Layer   │              │ Security / Sandbox      │
│ AGENTS map • docs • ADRs   │              │ least privilege • egress│
│ plans • examples • commands│              │ control • approvals     │
└───────────────┬────────────┘              └────────────┬────────────┘
                │                                        │
                ▼                                        ▼
┌────────────────────────────────────────────────────────────────────┐
│                    Context Compiler / Session Layer               │
│ sessions • working context • memory retrieval • compaction       │
│ handoff artifacts • scoped views per call / sub-agent            │
└───────────────┬───────────────────────┬────────────────────────────┘
                │                       │
                ▼                       ▼
┌────────────────────────────┐   ┌───────────────────────────────────┐
│      Orchestration Layer   │   │           Tool Plane              │
│ single-agent / workers /   │   │ CLI • MCP • code exec • browser   │
│ evaluator loops / handoffs │   │ search • APIs • RAG • artifacts   │
└───────────────┬────────────┘   └───────────────┬───────────────────┘
                │                                │
                └───────────────┬────────────────┘
                                ▼
┌────────────────────────────────────────────────────────────────────┐
│ Verification • Evaluation • Observability Layer                  │
│ tests • browser automation • eval suites • trace/log/metrics     │
│ evidence capture • quality scores • anomaly detection            │
└────────────────────────────────────────────────────────────────────┘
```

This architecture reflects the strongest cross-vendor convergence: repository legibility, compiled context, explicit tooling, live verification, and architectural containment.[1][4][6][7][8][10][11]

---

## 6. What “Best in Class” Looks Like in Practice

A best-in-class harness operating today usually has the following traits:

1. **Repository-first knowledge**: a short agent map, structured docs, examples, and execution plans in-repo.[1][3][9][10]
2. **Context compiler**: durable state separated from per-call views, with compaction and scoped handoffs.[2][4]
3. **High-quality tool catalog**: clear contracts, low overlap, runtime retrieval instead of context stuffing, and support for whichever interface best fits the tool surface, including CLI and MCP.[2][6][13]
4. **Structured state**: feature lists, progress logs, git commits, sessions/threads, and memory stores.[3][4][15]
5. **Layered verification**: deterministic gates where available, boundary checks where they pay off, and browser/API/system checks hitting the running artifact.[3][6][11][18][20][21][26]
6. **Strong sandboxing**: least privilege, network restrictions, config-file immutability, secret injection, and high-risk approvals.[7][8][10]
7. **Observability exposed to the agent**: trace/log/metric inspection and evidence capture.[6][10]
8. **Mechanical architecture enforcement**: CI-level boundaries, structural tests, linting with remediation cues.[1][10]
9. **Entropy management**: recurring doc and code cleanup agents.[1][10]
10. **Continuous harness pruning**: remove load-bearing assumptions that frontier models no longer need.[11]

---

## 7. What I Would Standardize If I Were Writing the Platform Spec

If you want a crisp platform spec, these are the defaults I would standardize:

### Required defaults

- **Short root instruction file** that maps the repo and points to deeper docs.[1][2][9]
- **Structured state artifacts** for long tasks: progress log, plan, feature list, and checkpoint commits.[3]
- **Governed tool integration** with a centrally approved catalog, per-tool scopes, and support for both CLI and MCP where appropriate.[6][13]
- **Context compilation pipeline** with selection, compaction, memory retrieval, and scoped handoffs.[2][4]
- **Layered verification suite** with deterministic gates plus browser/API/system checks where relevant.[3][11][18][20][21][26]
- **OpenTelemetry-compatible tracing** plus task-level cost telemetry.[6][7]
- **OS-level sandbox controls** plus approval workflow for dangerous actions.[7][8]
- **Mechanical architecture checks** in CI and recurring cleanup tasks.[1][10]

### Strongly recommended defaults

- evaluator/generator separation for subjective or high-ambiguity work;[11]
- ephemeral per-task or per-worktree environments;[10]
- stable prefix caching and output summarization;[2][4]
- model laddering or routing by difficulty/cost;[5][6]
- quality-score documents or domain scorecards tracked over time.[10]

### Optional / context-dependent defaults

- multi-agent orchestration;[5][16]
- context resets between sprints;[11]
- aggressive human approval on all risky steps versus only truly high-impact ones;[7][8]
- framework adoption versus direct API usage.[4][5][15]

---

# Part II — Predictions (Do **Not** Confuse With Current SoTA)

This section is intentionally forward-looking.

## 8. Where Current Research Is Leading

### Prediction 1: Harnesses will become **more compiler-like**, not less

Google’s “context as a compiled view” framing is likely to spread well beyond ADK because it solves a real scaling problem: prompt assembly becomes observable, testable, cache-aware, and model-agnostic.[4] I expect next-generation harnesses to treat context construction the way modern compilers treat IR pipelines: with named passes, typed state, deterministic transforms, and traceable outputs.

**Confidence: High.** This trend is already visible in Google ADK, Microsoft’s workflow/state abstractions, Anthropic’s structured handoffs, and practical multi-agent systems.[4][11][15]

### Prediction 2: Some orchestration scaffolding will disappear as models improve

Anthropic’s own trajectory is revealing: earlier harnesses needed explicit sprint decomposition and context resets, while newer models reduced the need for both.[11] The likely direction is that **better models reduce the need for human-authored decomposition scaffolding**, especially on medium-complexity tasks.

That does **not** mean the harness disappears. It means the harness shifts toward **verification, containment, and state management**, while hand-authored process logic shrinks.[11]

**Confidence: High.**

### Prediction 3: Learned policies and RL will start replacing some harness logic

Apple’s LOOP result is one of the strongest signals here. Their agent learned to consult API docs, avoid assumptions, recover from setbacks, and reduce confabulation through environment-grounded reinforcement learning rather than purely external scaffolding.[14] That suggests part of today’s harness burden may move into the model policy itself over time.

The most plausible near-term future is **hybridization**: externally enforced safety and state handling remain, while internalized model policies improve long-horizon planning and self-correction.[11][14]

**Confidence: Medium-High.** Strong research signal, but still not broad production consensus.

### Prediction 4: The tool abstraction will shift toward **richer action interfaces**

Today’s mainstream paradigm is structured tool calls plus browser/API-backed interaction surfaces, with MCP representing one prominent but still contested integration layer rather than a settled universal standard.[6][13] But research and practice are already pushing toward richer action channels:

- code-generation-as-action rather than JSON-only tool selection;[16]
- browser and computer-use loops with stronger environmental grounding;[10][11]
- workflow-native protocols such as MCP and emerging agent-to-agent interfaces.[13][15]

This means future harnesses will likely manage **heterogeneous action modes**: function calls, code execution, UI actions, agent handoffs, and artifact manipulation in one policy envelope.

**Confidence: Medium.**

### Prediction 5: Security will move from prompt guardrails to **environment-native trust systems**

OWASP and NVIDIA already point strongly away from prompt-only defenses and toward architectural containment, explicit tool permissions, and OS-level isolation.[7][8] The likely next step is a richer security fabric:

- signed tool identities;
- per-action policy evaluation;
- attested sandbox provenance;
- brokered ephemeral credentials;
- environment-native risk scoring for approvals.[7][8]

In other words, the harness will look more like a zero-trust runtime than a chatbot wrapper.

**Confidence: High.**

### Prediction 6: Shared memory and multi-agent coordination will become infrastructure problems

Today, shared context is handled via files, sessions, memory stores, and handoff artifacts.[3][4][11][15] As agent ecosystems mature, the hard problem becomes **coordination correctness**: who knows what, who can write what, what is canonical, and how conflicting agent updates reconcile.

I expect next-generation harnesses to adopt more explicit **identity, causality, and memory-consistency primitives** for multi-agent systems. Google’s A2A direction and framework-level session abstractions point toward this.[15]

**Confidence: Medium.**

### Prediction 7: “Repo legibility” will become a first-class engineering KPI

OpenAI’s public framing, GitHub’s `agents.md` guidance, and the broader move toward repository-as-system-of-record all point in the same direction: teams will increasingly measure how navigable and executable a repo is for agents.[1][9][10]

That likely means future engineering scorecards will track:

- bootstrap self-sufficiency;
- discoverability of task entry points;
- validation completeness;
- doc freshness;
- architecture map quality;
- decision-record coverage.[1][10]

**Confidence: High.**

---

## 9. What the Next Generation of LLMs Will Likely Be Capable Of

This is the practical forecasting section: what capabilities are plausible enough that your harness should be designed to accommodate them.

### 9.1 Better long-horizon self-repair

Models will likely get better at:

- consulting docs and tools before acting;[14]
- recognizing uncertainty and avoiding unwarranted assumptions;[14]
- recovering from failed attempts without derailment;[11][14]
- carrying intent across long tasks with less context anxiety.[11]

**Harness implication:** design for **structured recovery paths**, not brittle restart logic. Keep checkpoints and observability, but assume the model may increasingly repair itself.

### 9.2 Less need for hand-authored decomposition, more need for end-state validation

If model planning continues improving, the locus of harness value moves from “telling the agent how to work” to “proving the work is good.” Anthropic’s own simplification trajectory supports this.[11]

**Harness implication:** invest proportionally more in **evaluator quality, test environments, traceability, and evidence capture** than in elaborate task trees.

### 9.3 Stronger environment awareness

Agents will likely become better at navigating repos, UIs, and tool ecosystems with sparse hints rather than dense instructions.[2][10][11] But that makes **discoverability and naming quality** even more important, not less.

**Harness implication:** optimize file structure, naming, and tool ergonomics for autonomous exploration.

### 9.4 More competent cross-tool composition

As models improve at chaining tools, the bottleneck becomes not “can it call a tool” but “is the tool system safe, coherent, and queryable.”[5][6][13]

**Harness implication:** build the tool plane like a real platform: typed, permissioned, monitored, versioned.

### 9.5 More variance between “raw model capability” and “operational capability”

A stronger base model will increasingly expose weaknesses in the surrounding system: poor tool design, stale docs, weak sandboxing, missing observability, or bad approval UX.[1][6][8][10]

**Harness implication:** the differentiator will be less “which frontier model?” and more “how well-engineered is the runtime around it?”

---

## 10. My Forecast for the Next Evolution of Agentic AI Engineering

If I extrapolate from the best evidence rather than hype, the next phase of agentic engineering will likely have these characteristics:

### 10.1 From prompt engineering to **systems engineering**

The winning teams will think less about magic wording and more about:

- state machines,
- evidence pipelines,
- context compilation,
- permission systems,
- test harnesses,
- repo legibility,
- continuous cleanup.[1][4][6][7][10][11]

### 10.2 From static prompts to **programmable policy**

DSPy’s “programming, not prompting” thesis is important here even if it is not yet mainstream production doctrine.[16] The likely future is more typed modules, optimizable signatures, compiled behaviors, and fewer ad hoc prompt strings in critical paths.

### 10.3 From monolithic agents to **bounded agent fabrics**

The future is probably not one omniscient agent or dozens of free-roaming agents. It is a **fabric of bounded agents** with explicit scopes, typed handoffs, shared state contracts, and strong containment.[4][7][15]

### 10.4 From human review everywhere to **human review at leverage points**

As agent competence rises, humans will move up-stack:

- setting goals,
- defining policy,
- reviewing exceptions,
- tuning evaluators,
- judging ambiguous product tradeoffs.[1][10][11]

This is consistent with both OpenAI’s and Anthropic’s public direction, even though they differ on how aggressive autonomy should be today.[1][11]

### 10.5 From “agent as feature” to **agent as operating model**

The strongest public examples already show this. The harness is not just a chatbot integration; it becomes the way a team develops, validates, documents, and cleans up software.[1][10][11]

---

## 11. Recommended Build Strategy for a Serious Harness Team

If I were advising a team building an agent harness now, I would stage it like this:

### Phase 1 — Ship the non-negotiables

- repository map and progressive-disclosure docs;[1][2][9]
- governed tool catalog with permission scopes and explicit support for the interfaces your environment actually needs, which may include CLI, MCP, or both;[6][13]
- structured session/task artifacts and checkpointing;[3][4]
- live verification environment;[3][11]
- OpenTelemetry-style observability;[6][10]
- OS-level sandboxing and approval rules.[7][8]

### Phase 2 — Add quality multipliers

- generator/evaluator separation for ambiguous tasks;[11]
- context compaction and memory retrieval;[2][4]
- architecture linting and structural tests;[1][10]
- recurring cleanup and doc-gardening agents.[1][10]

### Phase 3 — Experiment carefully

- dynamic multi-agent routing;[5][15]
- learned evaluators or compiled prompt systems such as DSPy-like modules;[16]
- partial RL or self-improvement loops where a benchmarked environment exists;[14]
- richer agent-to-agent protocols and shared-state semantics.[15]

This sequencing matches the evidence: current wins come mostly from environment design, verification, and containment, not from maximal orchestration cleverness.[1][5][6][11]

---

## 12. Final Position

**SoTA today** is a harness that is:

- repository-legible,
- context-compiled,
- tool-disciplined,
- stateful across sessions,
- live-verified,
- deeply observable,
- architecturally contained,
- mechanically enforced,
- and continuously cleaned up.[1][2][3][4][6][7][8][10][11]

**The next evolution** is likely a harness that is:

- thinner in orchestration,
- stronger in verification and trust,
- more compiler-like in context handling,
- more platformized in tool and permission design,
- and increasingly paired with models that have learned some of today’s external scaffolding internally.[4][11][14][16]

That is the cleanest separation I can defend: **today’s production harness is still a systems discipline; tomorrow’s may become a systems-and-policy discipline with less hand-authored scaffolding but more formal runtime guarantees.**

---

## Confidence Assessment

### High confidence

- Context must be actively managed; bigger windows do not remove the need for context engineering.[2][4]
- Tool quality, overlap reduction, and token-efficient outputs are foundational.[2][6]
- Long-running agents need structured state artifacts and/or session abstractions.[3][4][11][15]
- Live verification beats code-only review for agent reliability.[3][6][11]
- External deterministic checkers improve trust only when the checked proposition and trust boundary are explicit; they complement rather than replace integration checks and live validation.[18][20][21][22][23][24][25][26][27]
- Security must be enforced architecturally with least privilege and sandboxing.[7][8]
- Repository legibility and progressive disclosure are increasingly central to harness quality.[1][2][9][10]

### Medium confidence

- Multi-agent systems will become more structured and protocolized rather than merely more numerous.[4][15]
- RL and learned policies will absorb some harness responsibilities over time, especially around recovery and doc consultation.[14]
- Typed/compiled prompt systems will gain share in serious agent engineering workflows.[16]

### Lower confidence / notable uncertainty

- Exactly how quickly planner/sprint scaffolding disappears as models improve.[11]
- Whether MCP becomes the dominant long-term tool substrate or one layer in a broader protocol stack.[13][15]
- How much of future reliability comes from better models versus better runtimes versus RL fine-tuning.[11][14][16]
- How far proof-oriented workflows will reach into mainstream application stacks, versus remaining concentrated in kernels, policies, math, and other well-specified subsystems.[20][21][22][23][25][27][28]

### Important caveat

The weakest part of the evidence base is direct primary-source retrieval from OpenAI’s original harness article due access blocking during research. I cross-checked those claims against your in-repo synthesis and reputable secondary coverage, but I weighted Anthropic, Google, AWS, Microsoft, GitHub, OWASP, NVIDIA, Apple, MCP, and DSPy primary materials more heavily when forming recommendations.[1][10][17]

I also excluded specific claims about Logical Intelligence in this revision because I could not verify them to the same standard as the formal-method systems cited below.

---

## References

1. `/Users/harry.nicholls/repos/xylem/docs/best-practices/harness-engineering.md:7-16,43-54,138-139,231-233,288-317,348-359,367-383,399-422`.
2. Anthropic, “Effective context engineering for AI agents,” https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents
3. Anthropic, “Effective harnesses for long-running agents,” https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents
4. Google Developers Blog, “Architecting efficient, context-aware multi-agent framework for production,” https://developers.googleblog.com/architecting-efficient-context-aware-multi-agent-framework-for-production/
5. Anthropic, “Building effective agents,” https://www.anthropic.com/research/building-effective-agents
6. AWS, “AI agents in enterprises: Best practices with Amazon Bedrock AgentCore,” https://aws.amazon.com/blogs/machine-learning/ai-agents-in-enterprises-best-practices-with-amazon-bedrock-agentcore/
7. OWASP, “AI Agent Security Cheat Sheet,” https://cheatsheetseries.owasp.org/cheatsheets/AI_Agent_Security_Cheat_Sheet.html
8. NVIDIA, “Practical security guidance for sandboxing agentic workflows and managing execution risk,” https://developer.nvidia.com/blog/practical-security-guidance-for-sandboxing-agentic-workflows-and-managing-execution-risk/
9. GitHub Blog, “How to write a great agents.md: Lessons from over 2,500 repositories,” https://github.blog/ai-and-ml/github-copilot/how-to-write-a-great-agents-md-lessons-from-over-2500-repositories/
10. Engineering.fyi, “Harness engineering: leveraging Codex in an agent-first world,” https://www.engineering.fyi/article/harness-engineering-leveraging-codex-in-an-agent-first-world ; InfoQ, “OpenAI Introduces Harness Engineering: Codex Agents Power Large-Scale Development,” https://www.infoq.com/news/2026/02/openai-harness-engineering-codex/ ; GitHub Blog, “GitHub Copilot coding agent 101,” https://github.blog/ai-and-ml/github-copilot/github-copilot-coding-agent-101-getting-started-with-agentic-workflows-on-github/
11. Anthropic, “Harness design for long-running apps,” https://www.anthropic.com/engineering/harness-design-long-running-apps
12. Anthropic, “Agent SDK overview,” https://platform.claude.com/docs/en/agent-sdk/overview
13. Model Context Protocol, “Introduction,” https://modelcontextprotocol.io/docs/getting-started/intro
14. Apple Machine Learning Research, “Reinforcement learning for interactive digital agents,” https://machinelearning.apple.com/research/reinforcement-learning-long-horizon
15. Microsoft Learn, “Agent Framework overview,” https://learn.microsoft.com/en-us/agent-framework/overview/ ; Google ADK docs, https://google.github.io/adk-docs/
16. `/Users/harry.nicholls/repos/xylem/docs/best-practices/harness-engineering.md:75-88,96-117,121-167,175-197,221-227,326-340,385-395`.
17. OpenAI direct page access was blocked during retrieval; corroborating summary used from `/Users/harry.nicholls/repos/xylem/docs/best-practices/harness-engineering.md:11-16,43-44,138-139,231-233,288-313,348-359,367-383` plus the secondary reporting in [10].
18. `/Users/harry.nicholls/repos/formal-verify/crosscheck/README.md:1-13,77-123,159-217,238-245`.
19. `/Users/harry.nicholls/repos/contract-graph-verifier/README.md:1-5,25-33,91-129`.
20. Disselkoen et al., “How We Built Cedar: A Verification-Guided Approach,” https://arxiv.org/html/2407.01688v1
21. AWS Open Source Blog, “Lean Into Verified Software Development,” https://aws.amazon.com/blogs/opensource/lean-into-verified-software-development/
22. Del Tredici et al., “Ax-Prover: A Deep Reasoning Agentic Framework for Theorem Proving in Lean,” https://arxiv.org/abs/2510.12787 ; Axiomatic AI Publications, https://axiomatic-ai.com/research/publications/
23. Harmonic, “Aristotle: IMO-level Automated Theorem Proving,” https://arxiv.org/html/2510.01346v1
24. Midspiral, “The correctness layer for AI-generated software,” https://midspiral.com/
25. Mistral AI, “Leanstral: Open-Source foundation for trustworthy vibe-coding,” https://mistral.ai/news/leanstral
26. AutoBE, https://autobe.dev
27. Harry Nicholls, “Formally verifying the easy part,” https://brainflow.substack.com/p/formally-verifying-the-easy-part
28. Leonardo de Moura, “AI Is Rewriting the World's Software,” https://leodemoura.github.io/blog/2026-2-28-when-ai-writes-the-worlds-software-who-verifies-it/
