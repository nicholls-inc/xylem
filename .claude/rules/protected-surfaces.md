Do not modify, delete, or move the configured protected control surfaces managed by the xylem control plane:
- .xylem/HARNESS.md
- .xylem.yml
- .xylem/workflows/*.yaml
- .xylem/prompts/*/*.md

Do not modify, delete, or move the module invariant specifications or their
property tests without an explicit human-authored amendment (see each spec's
"Governance" section). These are the load-bearing contracts for their modules
and must not be relaxed to make a failing test pass:
- docs/invariants/*.md
- cli/internal/*/invariants_prop_test.go
- cli/internal/*/*_invariants_prop_test.go
