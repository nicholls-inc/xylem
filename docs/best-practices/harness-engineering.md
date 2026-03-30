# The definitive guide to harness engineering for AI agent systems

**Harness engineering is the discipline of designing the entire execution environment around an LLM — tools, context, memory, feedback loops, constraints, and control systems — so that agents produce reliable, maintainable output at scale.** It subsumes context engineering (which focuses specifically on what tokens the model sees) while also covering orchestration, enforcement, security, and lifecycle management. This guide compiles every verified, source-backed best practice from the leading AI organizations as of March 2026. It is organized into 15 topic areas and distinguishes established consensus from emerging recommendations. Every claim traces to its source.

---

## 1. Harness engineering vs context engineering: definitions and relationship

### Harness engineering

- **Definition:** The discipline of designing environments, feedback loops, constraints, documentation, and control systems that enable AI agents to produce reliable work at scale. The human's role shifts from doing the work to designing the "harness" — the scaffolding and guardrails that keep agents productive and on track. (OpenAI — https://openai.com/index/harness-engineering/)
- **Mitchell Hashimoto's framing (credited as originator):** "Anytime you find an agent makes a mistake, you take the time to engineer a solution such that the agent never makes that mistake again." (OpenAI — https://openai.com/index/harness-engineering/)
- **Anthropic's definition:** The harness is everything wrapping an LLM — prompts, tool connections, inter-agent collaboration structures, feedback loops. "If the model is the engine, the harness is the car." (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **Microsoft's definition:** "The layer where model reasoning connects to real execution: shell and filesystem access, approval flows, and context management across long-running sessions." (Microsoft — https://learn.microsoft.com/en-us/agent-framework/overview/)
- **Martin Fowler's three categories of harness components:** (1) Context engineering — knowledge base + dynamic context like observability data; (2) Architectural constraints — deterministic linters + structural tests; (3) Entropy management / "garbage collection" — periodic agents finding inconsistencies. (Martin Fowler / Thoughtworks — https://martinfowler.com/articles/exploring-gen-ai/harness-engineering.html)
- **LangChain case study proving harness impact:** Improved deepagents-cli from 52.8% to **66.5% accuracy** on Terminal Bench 2.0 (outside Top 30 → Top 5) **without changing the model** (gpt-5.2-codex), exclusively through harness optimization of system prompts, tools, and middleware. (LangChain — https://blog.langchain.com/the-anatomy-of-an-agent-harness/)

### Context engineering

- **Definition:** The art and science of filling the context window with just the right information for the next step. It is the natural progression from prompt engineering — rather than crafting a single prompt, it manages the entire information ecosystem the model sees. (Andrej Karpathy, widely attributed; Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- **Core principle:** Find the *smallest possible* set of high-signal tokens that maximize the likelihood of the desired outcome. (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- **Relationship to harness engineering:** Context engineering is a *subset* of harness engineering. Harness engineering provides specific implementation guidance for context engineering while also covering broader topics like tool orchestration, security, error recovery, and lifecycle management. (HumanLayer — https://humanlayer.dev/blog/skill-issue-harness-engineering-for-coding-agents)

### Key distinction

- **[Established]** Context engineering asks: "What should the model see?" Harness engineering asks: "What entire environment does the agent need to reliably succeed?" Context engineering is a critical component of the harness, but the harness also includes enforcement mechanisms, lifecycle management, evaluation, and operational infrastructure.

---

## 2. Context window management

### Core strategies

- **Four canonical strategies (Write, Select, Compress, Isolate):**
  - **Write** — Persist context outside the window (scratchpads, memory files, progress docs). Anthropic's multi-agent researcher saves plans to memory to persist across 200K+ token truncation boundaries.
  - **Select** — Pull relevant context in via RAG, memory retrieval, or tool selection. RAG over tool descriptions can improve selection accuracy **3×** (arXiv:2505.03275).
  - **Compress** — Summarization and trimming. Claude Code runs "auto-compact" at **95% context window utilization**.
  - **Isolate** — Split context across sub-agents with focused windows. Anthropic's multi-agent researcher uses **15× more tokens** than chat but outperforms single-agent by giving each subagent a focused context window.
  - (LangChain — https://blog.langchain.com/context-engineering-for-agents/)

### Ordering and structure

- **Give the agent a map, not a 1,000-page instruction manual.** The "one big AGENTS.md" approach fails for four reasons: (1) context is scarce — a giant file crowds out the task; (2) too much guidance becomes non-guidance; (3) it rots instantly; (4) it's hard to verify mechanically. (OpenAI — https://openai.com/index/harness-engineering/)
- **Use progressive disclosure:** Short AGENTS.md (~100 lines) serves as a table of contents with pointers to deeper sources of truth in a structured `docs/` directory. Agents start with a small, stable entry point and are taught where to look next. (OpenAI — https://openai.com/index/harness-engineering/)
- **Organize system prompts with XML tags or Markdown headers** to establish clear sections the model can reference. Find the "Goldilocks zone" between brittle hardcoded logic and vague high-level guidance. (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- **Google's ADK principle — context is a "compiled view":** Separate durable state (Sessions) from per-call views (working context). Build context through named, ordered processors — not ad-hoc string concatenation — making the compilation observable and testable. (Google — https://developers.googleblog.com/architecting-efficient-context-aware-multi-agent-framework-for-production/)

### Compaction and pruning

- **Compaction (summarization in place):** When context nears limits, summarize older conversation events over a sliding window, write summary back as a new event, prune raw events. Implemented in Anthropic Claude Agent SDK, Google ADK, and OpenAI Codex. (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents; Google — https://developers.googleblog.com/architecting-efficient-context-aware-multi-agent-framework-for-production/)
- **Context reset vs. compaction:** Compaction preserves continuity but does NOT give a clean slate — "context anxiety" (premature wrap-up as model approaches perceived context limits) can persist. Context resets (clearing the window entirely with structured handoff) are more effective for mitigating context anxiety but add orchestration complexity, token overhead, and latency. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **[Emerging]** As models improve, compaction needs decrease. Anthropic found that Sonnet 4.5 required full context resets, Opus 4.5 largely removed the need, and Opus 4.6 runs continuously with automatic compaction — no resets needed. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **What to preserve during compaction:** Architectural decisions, unresolved bugs, implementation details. What to discard: redundant tool outputs, verbose intermediate reasoning. (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- **Fine-tuned summarization models** at agent-agent boundaries can improve compression quality over generic summarization. (Cognition AI, cited in LangChain — https://blog.langchain.com/context-engineering-for-agents/)

### Context failure modes

- **Context poisoning:** Hallucination enters context and propagates through subsequent reasoning.
- **Context distraction:** Too much context overwhelms the model's training signal.
- **Context confusion:** Superfluous context influences the response in unexpected ways.
- **Context clash:** Different parts of context contradict each other.
- (Drew Breunig, catalogued in LangChain — https://blog.langchain.com/context-engineering-for-agents/)
- **Context rot:** As tokens increase, the model's ability to accurately recall information decreases. Context is a finite resource with diminishing marginal returns. (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)

### Key insight

- **[Established]** "A focused 300-token context often outperforms an unfocused 113,000-token context." (FlowHunt, cited in LangChain — https://blog.langchain.com/context-engineering-for-agents/)

---

## 3. Tool and function calling design

### Design principles

- **Tools must be self-contained, robust to error, and have clear intended use.** "If a human can't say which tool to use in a given situation, the AI can't either." (Anthropic — https://www.anthropic.com/research/building-effective-agents)
- **Tool clarity matters more than brevity.** Bad: "Gets revenue data." Good: Full description with parameter types, return formats, and error conditions. (AWS — https://aws.amazon.com/blogs/machine-learning/ai-agents-in-enterprises-best-practices-with-amazon-bedrock-agentcore/)
- **Use clear, descriptive names and explicit parameters.** Every tool needs documentation of when and how to use it. For legacy systems without APIs, use computer-use models. (OpenAI — https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/)
- **Reduce ambiguity and overlap.** Avoid bloated tool sets with overlapping functionality — this causes more confusion than large tool counts. It's not just about count: some agents handle 15+ distinct tools well while others struggle with fewer than 10 overlapping tools. (OpenAI — https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/)
- **Tools must be token-efficient.** Design tool outputs to return only relevant data — tool bloat directly competes with task context. (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- **Use MCP (Model Context Protocol) for standardized tool integration.** Supported by Anthropic, Microsoft, Google, AWS, Mistral, and GitHub. Maintain a centralized approved tool catalog. (AWS — https://aws.amazon.com/blogs/machine-learning/ai-agents-in-enterprises-best-practices-with-amazon-bedrock-agentcore/)

### Advanced tool patterns

- **Just-in-time context via tools:** Rather than pre-loading all data, agents maintain lightweight identifiers (file paths, queries, links) and dynamically load data at runtime using tools. Claude Code uses this approach — drops CLAUDE.md files in context up front while using glob/grep for just-in-time retrieval. (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- **RAG over tool descriptions:** When agents have many tools, apply semantic search over tool descriptions to select the relevant subset. LangGraph's Bigtool demonstrates this. (LangChain — https://blog.langchain.com/context-engineering-for-agents/)
- **Custom lint error messages as tool-integrated teaching:** Custom lint error messages inject remediation instructions directly into agent context — "the tooling teaches the agent while it works." (OpenAI — https://openai.com/index/harness-engineering/)
- **[Emerging] CodeAct pattern (Apple research):** LLM agents that generate executable code rather than JSON tool calls — up to **20% higher success rate** across 17 LLMs. (Apple — https://machinelearning.apple.com)
- **Anti-pattern — tool thrash:** Don't micro-optimize which sub-agents access which tools. Over-constraining tool access leads to "tool thrash" with worse results. (HumanLayer — https://humanlayer.dev/blog/skill-issue-harness-engineering-for-coding-agents)

---

## 4. Memory systems

### Memory taxonomy

- **Three memory types:**
  - **Procedural** — Instructions, rules files (CLAUDE.md, AGENTS.md), system prompts. What the agent "knows how to do."
  - **Semantic** — Facts, knowledge graph entries, cross-session learned facts. What the agent "knows."
  - **Episodic** — Few-shot examples of desired behavior, past interaction patterns. What the agent "has experienced."
  - (LangChain — https://blog.langchain.com/context-engineering-for-agents/)

### Short-term memory

- **Thread-scoped checkpointing** within a session — current conversation state, intermediate results.
- **Structured note-taking** within a session: Agent writes persistent notes outside context window (e.g., NOTES.md, to-do lists), pulled back in when needed. Claude playing Pokémon demonstrates this across thousands of game steps. (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- **Scratchpads:** Tool call or state field for within-task note-taking. (Anthropic multi-agent researcher, cited in LangChain — https://blog.langchain.com/context-engineering-for-agents/)

### Long-term memory

- **Cross-session persistence** via structured artifacts: `claude-progress.txt` progress log, JSON feature lists, git commit history. Each new session reads these artifacts to get up to speed. (Anthropic — https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- **Repository knowledge as system of record:** All knowledge must be repository-local, versioned artifacts. Slack discussions, Google Docs, knowledge in people's heads are invisible to agents. "From the agent's point of view, anything it can't access in-context effectively doesn't exist." (OpenAI — https://openai.com/index/harness-engineering/)
- **Session-level key-value store** for maintaining structured, writable data agents can read/write — distinct from conversation history. (Google ADK — https://google.github.io/adk-docs/)
- **Cross-interaction memory** for task continuity and personalized experiences. (AWS Bedrock — https://docs.aws.amazon.com/bedrock/latest/userguide/agents-how.html)

### Memory security

- **Validate and sanitize data before storing** in memory systems. Implement memory isolation between users/sessions. Set expiration and size limits. (OWASP — https://cheatsheetseries.owasp.org/cheatsheets/AI_Agent_Security_Cheat_Sheet.html)

---

## 5. Prompt construction and system prompt design

### System prompt architecture

- **System prompt altitude:** Find the Goldilocks zone between brittle hardcoded logic and vague high-level guidance. (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- **Planners should stay high-level.** If a planner specifies granular technical details and gets something wrong, errors cascade into downstream implementation. "Constrain agents on the deliverables to be produced and let them figure out the path as they worked." (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **Provide a specific job/persona, exact commands to run, well-defined boundaries, and clear examples of good output.** "You are a test engineer who writes tests for React components, follows these examples, and never modifies source code" works. "You are a helpful coding assistant" doesn't. (GitHub — https://github.blog/ai-and-ml/github-copilot/how-to-write-a-great-agents-md-lessons-from-over-2500-repositories/)
- **Put commands early** in instruction files — include flags and options, not just tool names. (GitHub — https://github.blog/ai-and-ml/github-copilot/how-to-write-a-great-agents-md-lessons-from-over-2500-repositories/)
- **Code examples over explanations:** One real code snippet showing style beats three paragraphs describing it. (GitHub — https://github.blog/ai-and-ml/github-copilot/how-to-write-a-great-agents-md-lessons-from-over-2500-repositories/)

### Few-shot examples

- **Curate diverse, canonical examples** rather than stuffing edge cases. "Examples are pictures worth a thousand words." (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- **Calibrate evaluators with few-shot examples** including detailed score breakdowns to ensure judgment aligns with developer preferences. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)

### Instruction hierarchy

- **Multi-level instruction hierarchy:** Organization-level → repository-level → directory-level custom instructions. (GitHub — https://github.blog/ai-and-ml/github-copilot/how-to-write-a-great-agents-md-lessons-from-over-2500-repositories/)
- **AGENTS.md as table of contents:** Short (~100 lines), stable entry point that teaches agents where to look for deeper information. Links to structured `docs/` directory with design docs, product specs, execution plans, references, and domain-specific guides. (OpenAI — https://openai.com/index/harness-engineering/)

---

## 6. Agent loop design

### Core patterns (progressive complexity)

- **Start simple, increase complexity only when needed.** "Find the simplest solution possible, and only increase complexity when needed." Don't build agentic systems unless required. (Anthropic — https://www.anthropic.com/research/building-effective-agents)
- **Progressive pattern ladder:** Prompt chaining → Routing → Parallelization → Orchestrator-workers → Evaluator-optimizer → Full autonomous agents. (Anthropic — https://www.anthropic.com/research/building-effective-agents)
- **Avoid frameworks initially.** Start with LLM APIs directly. Many patterns are implementable in a few lines of code. Frameworks create abstraction layers that obscure prompts/responses and make debugging harder. (Anthropic — https://www.anthropic.com/research/building-effective-agents)

### Generator-evaluator separation

- **Separate the creator from the critic (GAN-inspired).** Agents are poor self-evaluators — when asked to evaluate their own work, they confidently praise it, even when quality is mediocre. Separating generator and evaluator roles is "more tractable than making generators self-critical." (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **Tuning a standalone evaluator to be skeptical** is far more tractable than making a generator critical of its own work. Once external feedback exists, the generator has something concrete to iterate against. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **Evaluator as a variable, not a constant:** The evaluator is worth the cost when the task sits beyond what the current model does reliably solo. As model capability increases, the evaluation boundary moves outward. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)

### Sprint-based and iterative execution

- **Sprint contracts:** Before each sprint, generator and evaluator negotiate a contract — agreeing on what "done" looks like before any code is written. Generator proposes what it will build and how success will be verified; evaluator reviews the proposal. They iterate until they agree. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **Getting-up-to-speed ritual:** Each session: (1) check current directory, (2) read git logs + progress files, (3) read feature list and choose highest-priority uncompleted feature. (Anthropic — https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- **Ralph Wiggum Loop (agent-to-agent review):** Agent reviews its own changes locally, requests additional agent reviews, responds to feedback, iterates in a loop until all reviewers are satisfied. Humans may review but aren't required to. (OpenAI — https://openai.com/index/harness-engineering/)

### Harness simplification over time

- **Every component encodes an assumption about what the model can't do.** Those assumptions are worth stress testing — they may be incorrect or go stale as models improve. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **Methodical simplification:** Remove one component at a time and review impact. Radical cuts (removing many things at once) make it difficult to tell which pieces were load-bearing. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **With each new model release, re-examine the harness:** Strip away pieces no longer load-bearing, add new pieces for new capabilities. The design space doesn't shrink — it moves. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)

---

## 7. Multi-agent coordination and delegation

### Orchestration patterns

- **Sequential:** Pipeline where each agent builds on previous output. Document review → translation → QA. (Microsoft — https://devblogs.microsoft.com/semantic-kernel/semantic-kernel-multi-agent-orchestration/)
- **Concurrent/Parallel:** Multiple agents work the same task simultaneously for ensemble reasoning, brainstorming, or voting. (Microsoft — https://devblogs.microsoft.com/semantic-kernel/semantic-kernel-multi-agent-orchestration/)
- **Supervisor/Orchestrator-workers:** Lead agent orchestrates specialized sub-agents, delegates tasks, aggregates results. (AWS — https://aws.amazon.com/solutions/guidance/multi-agent-orchestration-on-aws/; Anthropic — https://www.anthropic.com/research/building-effective-agents)
- **Handoff:** Dynamic delegation based on capabilities and task requirements. Agents transfer control to other agents. (Microsoft — https://devblogs.microsoft.com/semantic-kernel/semantic-kernel-multi-agent-orchestration/; Mistral — https://docs.mistral.ai/agents/introduction)
- **Group chat:** Agents engage in structured conversation to collaboratively solve problems. (Microsoft — https://devblogs.microsoft.com/semantic-kernel/semantic-kernel-multi-agent-orchestration/)
- **Magentic:** LLM-driven dynamic orchestration for adaptive workflows. (Microsoft — https://devblogs.microsoft.com/semantic-kernel/semantic-kernel-multi-agent-orchestration/)
- **A2A Protocol:** Google's Agent-to-Agent protocol for cross-framework agent communication. (Google — https://google.github.io/adk-docs/)

### Sub-agents as context firewalls

- **The primary motivation for multi-agent is context isolation, not specialization per se.** The dispatching agent only sees the prompt and final result — none of the intermediate tool calls. "Breaking work up into discrete tasks delegated to sub-agents keeps the primary coding agent in the 'smart zone.'" (HumanLayer — https://humanlayer.dev/blog/skill-issue-harness-engineering-for-coding-agents)
- **Sub-agents explore extensively** (tens of thousands of tokens) but return **condensed summaries** (1,000–2,000 tokens). This showed substantial improvement on complex research tasks. (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- **Trade-off:** Multi-agent uses up to **15× more tokens** than single-agent chat, but outperforms for complex tasks. (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)

### When to split into multi-agent

- **Split when:** Complex logic with many conditionals; tool overload (similar/overlapping tools cause confusion); tasks span distinct domains. (OpenAI — https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/)
- **[Established]** Multi-agent is not always better. Simpler single-agent harnesses outperform multi-agent for well-defined tasks. Use the simplest architecture that works. (Anthropic — https://www.anthropic.com/research/building-effective-agents)

### File-based agent communication

- **Agents communicate through files** (specs, progress docs, requirements) rather than message passing. One agent writes a file; another reads it and responds. Keeps work faithful to specifications without over-constraining implementation. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)

---

## 8. RAG integration

### Patterns

- **Agentic RAG (multi-stage retrieval):** Agent decides when to retrieve more documents. When initial retrieval doesn't find the answer, agent uses cross-reference extraction and continues retrieval iteratively. (Cohere — https://docs.cohere.com/docs/agentic-rag)
- **RAG as tool:** Wrap the retrieval pipeline as an agent tool, combined with other tools like code interpreters for post-processing. (Cohere — https://docs.cohere.com/docs/agentic-rag)
- **Hybrid retrieval for code:** Combination of embedding search, grep/file search, knowledge graph retrieval, and re-ranking. "Embedding search becomes unreliable as codebase size grows." (Windsurf, cited in LangChain — https://blog.langchain.com/context-engineering-for-agents/)
- **Document Library with citations:** Built-in RAG with citation support — model cites specific text spans from retrieved documents. Citations support "fast" vs "accurate" modes. (Mistral — https://docs.mistral.ai/agents/introduction; Cohere — https://docs.cohere.com/v2/docs/retrieval-augmented-generation-rag)
- **Knowledge bases in Bedrock:** Vector database integration for context-aware responses with citation mechanisms. (AWS — https://docs.aws.amazon.com/bedrock/latest/userguide/agents-how.html)

### RAG best practices

- **Use descriptive keys** (title, snippet, last_updated) in retrieved documents. Include only semantically relevant data. Use embedding endpoints for semantic search + rerank for precision. (Cohere — https://docs.cohere.com/v2/docs/retrieval-augmented-generation-rag)
- **[Established]** RAG context competes with task context for the same finite window. Retrieve minimally and precisely rather than flooding the window with tangentially relevant documents.

---

## 9. Evaluation and observability

### Evaluation

- **Instrument everything from day one.** Three layers: trace-level debugging → production dashboards → token usage/latency/error tracking. OpenTelemetry traces for model invocations, tool calls, and reasoning steps. (AWS — https://aws.amazon.com/blogs/machine-learning/ai-agents-in-enterprises-best-practices-with-amazon-bedrock-agentcore/)
- **Automate evaluation from the start.** Balance technical metrics (latency, accuracy) with business metrics. Include multiple phrasings of same question, edge cases, and ambiguous queries. Run evaluations after every change. (AWS — https://aws.amazon.com/blogs/machine-learning/ai-agents-in-enterprises-best-practices-with-amazon-bedrock-agentcore/)
- **Evaluator uses the live application.** Anthropic's evaluator uses Playwright MCP to click through the running application like a real user — testing UI features, API endpoints, and database states, not just reading code. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **Calibrate evaluator with human judgment:** Read evaluator logs → find where judgment diverges from human judgment → update evaluator prompt. Takes several rounds. Out of the box, Claude is a poor QA agent: it identifies legitimate issues then talks itself into approving work anyway. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **Verifier's Law:** "The ability to train an AI for a task is proportional to how easily verifiable that task is." (Jason Wei, cited in LangChain — https://blog.langchain.com/context-engineering-for-agents/)
- **Future-proofing test:** If performance scales up with more powerful models without harness changes, your harness is well-designed. (LangChain — https://blog.langchain.com/the-anatomy-of-an-agent-harness/)
- **DSPy evaluation framework:** Built-in metrics (exact match, F1, passage match) with optimizers (MIPROv2, BootstrapFewShot) that automatically optimize prompts and weights. Compiled pipelines can outperform expert-created demonstrations. (Stanford — https://dspy.ai/)

### Observability

- **Local observability stack, ephemeral per worktree:** Logs, metrics, and traces exposed to agents. Agents query logs with LogQL and metrics with PromQL. Fully isolated — torn down when task completes. Enables prompts like "ensure service startup completes in under 800ms." (OpenAI — https://openai.com/index/harness-engineering/)
- **Make the application inspectable by agents:** Boot the app per git worktree so the agent can launch and drive one instance per change. Wire Chrome DevTools Protocol into agent runtime for DOM snapshots, screenshots, navigation, bug reproduction, and fix validation. (OpenAI — https://openai.com/index/harness-engineering/)
- **Quality scoring document:** Grades each product domain and architectural layer, tracking gaps over time. Updated by agents regularly. (OpenAI — https://openai.com/index/harness-engineering/)
- **Ground truth dataset (50+ expected interactions)** as a baseline for evaluation. (AWS — https://aws.amazon.com/blogs/machine-learning/ai-agents-in-enterprises-best-practices-with-amazon-bedrock-agentcore/)

---

## 10. Error handling, retry logic, and graceful degradation

### Agent failure as a system signal

- **"When the agent struggles, treat it as a signal: identify what is missing — tools, guardrails, documentation — and feed it back into the repository."** The fix is almost never "try harder." Ask: "What capability is missing, and how do we make it both legible and enforceable for the agent?" (OpenAI — https://openai.com/index/harness-engineering/)
- **Define expected behavior for each failure mode** — retry, fallback, or report unavailable. (AWS — https://aws.amazon.com/blogs/machine-learning/ai-agents-in-enterprises-best-practices-with-amazon-bedrock-agentcore/)
- **Set limits on agent retries and actions.** Escalate if failure thresholds are exceeded. (OpenAI — https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/)

### Specific recovery patterns

- **Feature list with pass/fail status** prevents agents from declaring victory too early. Agents only change status, never remove or edit features. JSON used over Markdown because the model is less likely to inappropriately modify JSON. (Anthropic — https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- **End-to-end testing via browser automation** catches features marked done prematurely. Without explicit prompting, Claude makes code changes but fails to verify features work end-to-end. Adding Puppeteer/Playwright MCP dramatically improves verification. (Anthropic — https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- **Git commits as recovery points:** Descriptive commit messages after each feature allow rollback and provide state context for the next session. (Anthropic — https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- **Test flakes addressed with follow-up runs** rather than blocking progress indefinitely. "In a system where agent throughput far exceeds human attention, corrections are cheap, and waiting is expensive." (OpenAI — https://openai.com/index/harness-engineering/)

### ⚠️ Area of divergence

- **OpenAI favors minimal blocking merge gates** — pull requests are short-lived, test flakes handled with follow-up runs. They acknowledge this would be "irresponsible in a low-throughput environment." (OpenAI — https://openai.com/index/harness-engineering/)
- **Anthropic favors hard evaluation thresholds** — each criterion has a hard threshold; if any falls below, the sprint fails and the generator gets detailed feedback to fix issues. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **Synthesis:** OpenAI's approach reflects a greenfield, high-throughput, agent-first environment. Anthropic's approach is more conservative and appropriate for mixed human-agent teams or production systems with higher stakes. Choose based on your trust boundary and blast radius.

---

## 11. Security, sandboxing, and trust boundaries

### Architectural security

- **Architectural containment over prompt-based defenses.** Prompt injection is treated as an unsolved problem — security must come from architecture, not prompt instructions. (OWASP — https://cheatsheetseries.owasp.org/cheatsheets/AI_Agent_Security_Cheat_Sheet.html)
- **Plan-then-Execute pattern:** Decouple planning from execution — even if the LLM is hijacked, it can only execute pre-approved plans with least-privilege tools in a sandbox. (arXiv:2509.08646)
- **Structured output enforcement** (JSON with strict schema) prevents unexpected content generation. (OWASP — https://cheatsheetseries.owasp.org/cheatsheets/AI_Agent_Security_Cheat_Sheet.html)

### Sandboxing

- **Action sandboxing:** Docker containers, VMs, gVisor, Kata containers for code execution isolation. Full virtualization preferred over shared-kernel solutions. (NVIDIA — https://developer.nvidia.com/blog/practical-security-guidance-for-sandboxing-agentic-workflows)
- **GitHub Copilot sandboxing model:** Runs in GitHub Actions environment with restricted internet access and limited repository permissions. Can only push to branches it creates (e.g., `copilot/*`). All PRs require independent human review. CI/CD checks need approval. Commits are co-authored for traceability. (GitHub — https://github.blog/ai-and-ml/github-copilot/github-copilot-coding-agent-101-getting-started-with-agentic-workflows-on-github/)

### Trust boundaries

- **Least privilege:** Per-tool permission scoping, separate tool sets for different trust levels. (OWASP — https://cheatsheetseries.owasp.org/cheatsheets/AI_Agent_Security_Cheat_Sheet.html)
- **Human-in-the-loop required** for high-impact decisions (financial transactions, production database writes, irreversible actions). (OpenAI — https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/; OWASP)
- **Instructions come from developers, not users.** Model should be trained to obey system instructions over user prompts. "It's best not to interpolate untrusted user input into the instructions." (Apple — https://developer.apple.com/videos/play/wwdc2025/286/)
- **Safety levels in agent configuration:** Mistral's Vibe coding agent offers configurable levels: safe/neutral/destructive/yolo, with custom tool permissions. (Mistral — https://mistral.ai/news/agents-api)
- **Guardrails:** Input filtering (prompt injection prevention, relevance validation, keyword filtering), tool-use guardrails, and human-in-the-loop intervention. (OpenAI — https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/)

---

## 12. Long-running agent task management

### Session-to-session continuity

- **Initializer + Worker pattern:** Specialized first-run prompt sets up the environment (init.sh script, progress log, feature list, initial git commit). Every subsequent session makes incremental progress and leaves structured updates. (Anthropic — https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- **Single Codex runs regularly work for upwards of six hours** (often while humans sleep). (OpenAI — https://openai.com/index/harness-engineering/)
- **Structured handoff artifacts** carry previous agent's state and next steps when using context resets — includes what was completed, what failed, and what's next. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)

### Context anxiety mitigation

- **Context anxiety** is a model behavior where agents begin wrapping up work prematurely as they approach what they believe is their context limit. Strongly observed in Claude Sonnet 4.5. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **Context resets (clearing + structured handoff) are more effective than compaction alone** for mitigating context anxiety. Compaction preserves continuity but doesn't reset the model's perception of how "full" the context is. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **[Emerging]** Newer models (Opus 4.5, Opus 4.6) show significantly reduced context anxiety, potentially eliminating the need for resets entirely. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)

### Task decomposition

- **Sprints with contracts for complex tasks:** Before building, generator and evaluator negotiate what "done" looks like. Each sprint has hard evaluation thresholds. Failed sprints return detailed feedback. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **[Emerging]** With Opus 4.6, Anthropic removed the sprint construct entirely — the model could handle work without decomposition. The evaluator moved to a single pass at the end. This suggests sprint-based decomposition may become unnecessary as models improve. (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)

---

## 13. State management and checkpointing

### State persistence mechanisms

- **Git as primary checkpoint mechanism:** Descriptive commit messages after each feature. Provides state context for next session, enables rollback, and creates an audit trail. (Anthropic — https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- **Progress files:** `claude-progress.txt` as session-to-session log. Read at start of each session along with git history. (Anthropic — https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- **Feature lists (JSON):** Over 200 features tracked with `passes: false/true` status. Agents only change status — never remove or edit features. JSON used instead of Markdown because the model is less likely to inappropriately modify structured JSON. (Anthropic — https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- **Execution plans as first-class artifacts:** Active plans, completed plans, and known technical debt — all versioned and co-located in the repository. (OpenAI — https://openai.com/index/harness-engineering/)

### Framework-level state management

- **Google ADK Sessions:** Full structured state with key-value store distinct from conversation history — structured, writable data agents can read/write. Cross-session via Memory objects. (Google — https://google.github.io/adk-docs/)
- **Microsoft Agent Thread:** Core abstraction for conversation state. Supports stateful service-side threads and local in-memory threads. Agent Skills as portable, reusable packages that agents discover and load at runtime. (Microsoft — https://learn.microsoft.com/en-us/agent-framework/overview/)
- **AWS SessionState:** SessionAttribute for Lambda-only info, SessionPromptAttribute for prompt-level info. Cross-interaction memory for task continuity. (AWS — https://docs.aws.amazon.com/bedrock/latest/userguide/agents-how.html)

---

## 14. Cost optimization and token budgeting

### Token efficiency strategies

- **Context compaction at thresholds:** Claude Code runs auto-compact at 95% context window utilization. Google ADK triggers compaction at configurable thresholds. (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents; Google — https://developers.googleblog.com/architecting-efficient-context-aware-multi-agent-framework-for-production/)
- **Sub-agents as context firewalls:** Parent agent only sees prompt + final result, not intermediate tool call noise. Prevents token accumulation from tool-heavy subtasks. (HumanLayer — https://humanlayer.dev/blog/skill-issue-harness-engineering-for-coding-agents)
- **Tool output summarization** before re-injection into context reduces waste from verbose tool responses. (LangChain — https://blog.langchain.com/context-engineering-for-agents/)
- **Model selection ladder:** Build prototype with most capable (expensive) model → establish baseline → swap smaller models to test if results are acceptable → optimize cost/latency. (OpenAI — https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/)
- **Caching previous plans:** Store subtask plans in vector store to avoid redundant LLM calls for similar tasks. (PromptingGuide.ai — https://promptingguide.ai/guides/context-engineering-guide)

### Cost benchmarks

- **Anthropic DAW example (Opus 4.6):** Full 3-agent harness producing a working browser-based DAW: Planner ($0.46, 4.7 min) + Build Round 1 ($71.08, 2h 7min) + QA ($3.24) + Build Round 2 ($36.89) + two more QA/build cycles = **$124.70 total, 3h 50min.** (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **Anthropic Retro Game Maker (Opus 4.5):** Solo agent: $9, 20 min (broken result). Full 3-agent harness: $200, 6 hr (working result). (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)
- **"Cost scales with ambition": $200 for a working app is expensive for a demo but cheap for a product.** (Anthropic — https://www.anthropic.com/engineering/harness-design-long-running-apps)

### ⚠️ Key trade-off

- Multi-agent systems use up to **15× more tokens** than single-agent chat. The quality improvement justifies cost only for tasks beyond what a single agent handles reliably. (Anthropic — https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)

---

## 15. Architectural enforcement and entropy management

### Enforcing constraints

- **Enforce invariants, not implementations.** Example: require agents to "parse data shapes at the boundary" (parse, don't validate principle) but don't prescribe how — the model chooses its own approach (e.g., Zod). (OpenAI — https://openai.com/index/harness-engineering/)
- **Strict layered architecture** enforced mechanically: `Types → Config → Repo → Service → Runtime → UI`. Cross-cutting concerns (auth, connectors, telemetry, feature flags) enter through a single explicit interface: Providers. Everything else is disallowed and enforced at CI level. (OpenAI — https://openai.com/index/harness-engineering/)
- **"In a human-first workflow, these rules might feel pedantic. With agents, they become multipliers: once encoded, they apply everywhere at once."** (OpenAI — https://openai.com/index/harness-engineering/)
- **"Enforce boundaries centrally, allow autonomy locally."** Like leading a large engineering platform organization — care deeply about boundaries and correctness; allow agents significant freedom in how solutions are expressed within those boundaries. (OpenAI — https://openai.com/index/harness-engineering/)

### Entropy management / garbage collection

- **Codex replicates patterns that already exist — even uneven or suboptimal ones — leading to drift.** Initially, the team spent every Friday (20% of the week) cleaning up "AI slop." This didn't scale. (OpenAI — https://openai.com/index/harness-engineering/)
- **Solution — "golden principles" + background cleanup agents:** Opinionated, mechanical rules for codebase legibility encoded in the repository. Background agents run on a regular cadence: scan for deviations, update quality grades, open targeted refactoring PRs (most reviewable in under a minute, automerged). (OpenAI — https://openai.com/index/harness-engineering/)
- **"Human taste is captured once, then enforced continuously on every line of code."** Each engineer's domain expertise (React patterns, security, architecture) is encoded once and then applied by all agents across the entire codebase. (OpenAI — https://openai.com/index/harness-engineering/)
- **Dedicated doc-gardening agent** scans for stale/obsolete documentation and opens fix-up PRs. Linters and CI jobs validate the knowledge base is up to date, cross-linked, and structured correctly. (OpenAI — https://openai.com/index/harness-engineering/)
- **"Technical debt is like a high-interest loan: it's almost always better to pay it down continuously in small increments than to let it compound."** (OpenAI — https://openai.com/index/harness-engineering/)

---

## 16. Additional patterns and emerging best practices

### Repo legibility scoring

Seven legibility metrics for scoring how agent-ready a repository is (OpenAI — https://openai.com/index/harness-engineering/):
1. **Bootstrap self-sufficiency** — Can the repo get set up from scratch without external knowledge?
2. **Task entry points** — Can the agent find and run "build," "test," "lint," "run" commands?
3. **Validation harness** — Can the agent check whether its changes work?
4. **Linting and formatting** — Automated code quality rules
5. **Codebase map** — High-level guide showing the agent where things are
6. **Doc structure** — Docs organized so the agent can find what it needs
7. **Decision records** — Past architectural decisions written in the repo

### Technology selection for agent systems

- **Favor "boring" technologies** that are easier for agents to model due to composability, API stability, and training-set representation. Sometimes cheaper to have the agent reimplement subsets of functionality rather than work around opaque upstream behavior. (OpenAI — https://openai.com/index/harness-engineering/)
- **Optimize first for agent legibility, not human readability.** The resulting code does not always match human stylistic preferences, and that's okay. "As long as the output is correct, maintainable, and legible to future agent runs, it meets the bar." (OpenAI — https://openai.com/index/harness-engineering/)

### Increasing levels of autonomy

End-to-end agent autonomy demonstrated at OpenAI — given a single prompt, the agent can: validate codebase state → reproduce bug → record video → implement fix → validate fix → record resolution video → open PR → respond to feedback → detect and remediate build failures → escalate to human only when judgment is required → merge. **Caveat: "This behavior depends heavily on the specific structure and tooling of this repository and should not be assumed to generalize without similar investment."** (OpenAI — https://openai.com/index/harness-engineering/)

### Agentic Context Engineering (ACE) framework

- **[Emerging — academic]** Researchers from Stanford, SambaNova, and UC Berkeley propose contexts as evolving "playbooks" developed through modular generation, reflection, and curation. Three components: Generator (produces reasoning traces), Reflector, and Curator. Addresses "context collapse" where repeated rewriting loses detail. (Stanford/Berkeley — cited in InfoQ, https://www.infoq.com/news/2025/10/agentic-context-eng/)

### DSPy — programming over prompting

- **[Emerging — academic]** Stanford NLP's framework for *programming* rather than *prompting* LLMs. Typed I/O contracts (Signatures), composable modules (Predict, ChainOfThought, ReAct), and automated optimizers (MIPROv2, BootstrapFewShot) that optimize prompts and weights. A 770M T5 model compiled with DSPy is competitive with GPT-3.5 on some tasks. (Stanford — https://dspy.ai/)

### Apple's reinforcement learning for long-horizon agents

- **[Emerging — research]** LOOP: Data/memory-efficient PPO variant where a 32B agent outperforms OpenAI o1 by **9 percentage points** in AppWorld. Agent learns to consult API docs, avoid assumptions, minimize confabulation, and recover from setbacks — all through RL rather than harness scaffolding. (Apple — https://machinelearning.apple.com/research/reinforcement-learning-long-horizon)

---

## Summary: where sources agree and where they diverge

### Strong consensus across all sources

- Context is a finite, precious resource — manage it actively rather than dumping everything in
- Tools must have clear descriptions, minimal overlap, and token-efficient outputs
- Every agent failure is a system-level signal to improve the harness, not a reason to "try harder"
- Start simple; add complexity only when measured results justify it
- Mechanical enforcement (linters, CI, structural tests) beats guidelines in documents
- State persistence across sessions requires structured artifacts (progress files, feature lists, git history)
- Evaluation must test the live system, not just review code

### Areas of divergence

| Topic | Position A | Position B |
|-------|-----------|-----------|
| **Merge gates** | OpenAI: Minimal blocking gates; corrections are cheap at high throughput | Anthropic/AWS: Hard evaluation thresholds; sprint fails if any criterion falls below |
| **Sprint decomposition** | Anthropic (Opus 4.5): Sprint contracts with iterative evaluation | Anthropic (Opus 4.6): Removed sprints entirely; single-pass evaluation at end |
| **Compaction vs. reset** | Google ADK / Anthropic (later models): Compaction within session is sufficient | Anthropic (earlier models): Full context resets needed to address context anxiety |
| **Framework usage** | Anthropic: "Avoid frameworks initially. Start with LLM APIs directly." | Microsoft/Google/AWS: Build on their agent frameworks (Semantic Kernel, ADK, Bedrock) |
| **Harness complexity trajectory** | Anthropic: "Harness complexity should decrease over time as models improve" | OpenAI: Harness becomes more sophisticated over time as more team expertise gets encoded |
| **RL vs. harness scaffolding** | Apple LOOP: Train the model itself to recover from setbacks via RL | OpenAI/Anthropic: Build harness infrastructure to prevent and handle failures externally |

The resolution: these divergences largely reflect different contexts (greenfield vs. brownfield, coding agents vs. general agents, frontier models vs. smaller models) rather than true disagreements about principles. **The universal meta-principle is: match harness complexity to the gap between model capability and task requirements, and continuously reassess as models improve.**