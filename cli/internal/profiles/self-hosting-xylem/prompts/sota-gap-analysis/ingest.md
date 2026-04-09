Prepare the weekly SoTA harness gap-analysis run for xylem.

Read these reference docs:

1. `docs/best-practices/harness-engineering.md`
2. `docs/design/sota-agent-harness-spec.md`

Read the live implementation surfaces under `cli/internal/` and the control-plane entry points under:

1. `cli/cmd/xylem/`
2. `cli/internal/runner/`
3. `cli/internal/scanner/`

Your goal in this phase is to produce a concise inventory that the next phase can rely on.

The inventory must include:

1. The ten harness capability buckets to score:
   - context
   - tools
   - memory
   - orchestration
   - verification
   - evaluation
   - observability
   - security
   - entropy
   - cost
2. The concrete spec/doc sections that define each bucket.
3. The most relevant implementation files and packages for each bucket.
4. A short note on whether each bucket looks primary-path wired, standalone-but-dormant, or absent.

Keep the output factual and cite file paths inline so the survey phase can convert it into the structured snapshot.
