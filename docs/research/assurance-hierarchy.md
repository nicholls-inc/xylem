# A Six-Layer Assurance Hierarchy for AI-Assisted Software Development

## Scope

This hierarchy addresses the question: **given a specification, can we be confident the implementation is correct?** It explicitly excludes whether the specification solves the right problem — that is a product discovery concern, not an engineering assurance concern.

## The Hierarchy

### Layer 1: Formally Verified Pure, Functional Code

Business logic and core algorithms are written in a formally verifiable language such as Dafny, which compiles to target languages including JavaScript and TypeScript. The formal verification toolchain (Dafny's verifier, or Lean 4 via tools like Axiom or Harmonic's Aristotle) provides mathematical proof that the code satisfies its specification for all possible inputs — not just tested inputs.

This is deterministic assurance: the proof either passes or it doesn't.

### Layer 2: Compilation Correctness

Formal verification proves properties of source code, but the deployed system runs compiled output. If the compiler has a bug, the emitted code may not preserve the properties proven in the source language. This layer provides assurance that the compilation or transpilation step preserves verified properties.

Approaches include verified compilation (e.g., CompCert for C), translation validation (verifying each specific compilation output against its source), or proof-carrying code where proofs travel with the compiled artifact.

Without this layer, a formally verified Dafny program and its compiled JavaScript output are connected only by trust in the compiler — an unverified link in an otherwise verified chain.

### Layer 3: Contract Graph Verification

Individual verified units must compose correctly. The contract graph verifier checks interface contracts at integration boundaries — between verified units and between verified code and third-party libraries. Critically, this verification operates end-to-end across subgraphs, not just at pairwise boundaries.

This distinction matters because two units can each satisfy their boundary contracts while producing emergent behavior that neither unit's specification anticipated — ordering dependencies, resource contention, or feedback loops that only manifest in the full assembly. End-to-end subgraph verification catches these composition failures.

This layer targets the approximately 75% of code surface area that sits at integration boundaries, which resists formal proof of individual units and is where the majority of production bugs originate.

### Layer 4: Implementation–Specification Alignment

Layers 1–3 prove that code satisfies a specification. This layer asks: does the specification actually describe the implementation's behavior? Tools like Midspiral's lemmafit integrate Dafny verification into the development workflow, where code that cannot be proven correct against the specification does not compile.

This is still deterministic — the verifier accepts or rejects.

### Layer 5: Specification–Intent Alignment

A verified proof guarantees the code is correct relative to a specification, but does the specification capture what was actually intended? This is the gap between "verified" and "intended."

Midspiral's claimcheck addresses this via round-trip informalization: translate the formal specification back into plain English without seeing the original requirement, then compare the back-translation against the original intent. In testing, this caught both planted errors and unexpected gaps — for example, a requirement stating "adding a ballot can't decrease a tally" where the lemma merely proved counts are non-negative, a tautology masquerading as a monotonicity guarantee.

This layer is probabilistic. Claimcheck reports approximately 96.3% accuracy using structural separation (two models, with the informalizer blind to the original requirement), though this is acknowledged as a development benchmark rather than a formal evaluation.

### Layer 6: Specification Completeness

Even if every specified property is correctly implemented and aligned with intent, the specification itself may be incomplete — it may fail to enumerate properties that matter. Traditional user stories test the happy path and some error cases, getting nowhere near the exhaustive coverage that formal verification provides.

The concept of formally verified user stories addresses this: rather than writing a handful of test cases from a user story, an LLM systematically enumerates candidate formal properties — invariants, pre/post conditions, boundary behaviors, commutativity, monotonicity, conservation laws. A human reviews the property list, each property is formally verified, and claimcheck validates intent alignment.

This layer is best-effort. There is no theorem that can prove a specification is complete — the question "have I missed any important properties?" is inherently a human judgment call. However, adversarial property discovery — where one agent proposes properties and another agent tries to find scenarios the property set doesn't cover — could make this search significantly more structured than the status quo.

## Two Chains, One Gradient

The hierarchy contains two distinct chains with different failure modes and verification methods:

**The Implementation Chain (Layers 1–3):** Is the code correct and do the pieces fit together? This chain is deterministically verifiable via formal proofs and contract checks.

**The Specification Chain (Layers 4–6):** Does the spec match the code, the intent, and the full problem surface? This chain degrades from deterministic (Layer 4) to probabilistic (Layer 5, ~96%) to best-effort (Layer 6).

Residual risk concentrates at the top of the specification chain — Layer 6 — which is where the research opportunity lies.

## Supporting Workflow Elements

Two additional practices sit alongside the hierarchy rather than within it:

**External Acceptance Oracle.** Deterministic verification steps written from the perspective of a user, exercising the feature to determine if the user story is satisfied. These exist outside the repository so the coding agent cannot write code to pass them, and are written before development starts to force upfront intent specification. Requirements must be mechanically verifiable — subjective criteria like "the page feels responsive" must be quantified. The oracle provides empirical, user-perspective assurance that the service works, complementing the exhaustive assurance of formal verification.

**Test Theatre Detection.** Critical review of automated tests to eliminate tests that look productive but verify nothing meaningful. This runs after the implementation has stabilized — not as a sequential pipeline step during development, but as a quality gate once there is confidence the code has stopped changing.

## References

- Axiom. AXLE - Axiom Lean Engine. https://axle.axiommath.ai/
- Axiom. https://axiommath.ai/
- Harmonic. Aristotle API. https://aristotle.harmonic.fun/
- Harmonic. "Aristotle: IMO-level Automated Theorem Proving." arXiv, March 2026. https://arxiv.org/html/2510.01346v1
- Midspiral. https://midspiral.com/
- Midspiral. "lemmafit: Make agents prove that their code is correct." GitHub. https://github.com/midspiral/lemmafit
- Midspiral. "claimcheck: Narrowing the Gap between Proof and Intent." https://midspiral.com/blog/claimcheck-narrowing-the-gap-between-proof-and-intent/
- Midspiral. "From Intent to Proof: Dafny Verification for Web Apps." https://midspiral.com/blog/from-intent-to-proof-dafny-verification-for-web-apps/
- Kleppmann, M. "Prediction: AI will make formal verification go mainstream." December 2025. https://martin.kleppmann.com/2025/12/08/ai-formal-verification.html
- de Moura, L. "When AI Writes the World's Software, Who Verifies It?" February 2026. https://leodemoura.github.io/blog/2026/02/28/when-ai-writes-the-worlds-software.html
