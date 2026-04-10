Classify the unlabeled issues discovered in the previous phase and produce a deterministic labeling plan.

## Discover manifest
{{.PreviousOutputs.discover}}

Output JSON only. Do not wrap it in code fences and do not add commentary before or after the JSON.

Use the repository's existing label set from the discover manifest. Never invent labels that are not present there.

Default xylem heuristics if those labels exist in the repo:

- Type labels: `bug`, `enhancement`, `documentation`, `question`, `testing`
- Component labels: `cli`, `workflows`, `compiler`, `mcp`, `security`, `performance`
- Priority/community labels: `priority-high`, `good first issue`, `community`
- Fallback label: `needs-triage`

Conservative rules:

1. Prefer missing a component label over guessing one.
2. If confidence is below `0.80`, or the issue could reasonably fit multiple conflicting labels, include `needs-triage` and set `fallback` to `true`.
3. Only apply `community` when the author is not a bot and `author_association` is not `OWNER`, `MEMBER`, or `COLLABORATOR`.
4. Only apply `good first issue` when the change is narrow, low-risk, and suitable for a newcomer.
5. Only apply `priority-high` for issues that appear release-blocking, security-sensitive, data-loss related, or severe user-facing breakage.
6. If a label category is unavailable in the repo label set, skip it rather than substituting a different label.
7. Every issue must receive at least one label. If nothing else is safe, use only `needs-triage`.

Return this schema:

```json
{
  "issues": [
    {
      "number": 123,
      "title": "Example",
      "url": "https://github.com/owner/repo/issues/123",
      "labels": ["bug", "cli"],
      "confidence": 0.93,
      "fallback": false,
      "rationale": "Clear bug report against CLI behavior with concrete reproduction steps.",
      "signals": ["mentions panic", "touches command output", "member-authored"]
    }
  ],
  "summary": {
    "processed": 1,
    "auto_labeled": 1,
    "needs_triage": 0,
    "community": 0
  }
}
```

Deduplicate labels per issue and keep them in a stable order: type/community first, then component labels, then priority/fallback labels.
