Prepare the semantic-refactor path for the recurring continuous-refactoring workflow.

Mode: `{{index .Source.Params "mode"}}`
Source dirs: `{{index .Source.Params "source_dirs"}}`
File extensions: `{{index .Source.Params "file_extensions"}}`
Exclude patterns: `{{index .Source.Params "exclude_patterns"}}`
Max issues per run: `{{index .Source.Params "max_issues_per_run"}}`

If the mode is not `semantic_refactor`, do not inspect the repo. Output exactly:

```
Semantic refactor path skipped for mode {{index .Source.Params "mode"}}.
```

If the mode is `semantic_refactor`:

1. Inspect the configured directories and non-test source files.
2. Group files by their apparent responsibility.
3. Identify up to `{{index .Source.Params "max_issues_per_run"}}` high-signal candidates where:
   - a function or tightly related function cluster appears misplaced,
   - duplicate or near-duplicate logic is likely present, or
   - a file's semantic theme is diluted by unrelated helpers.
4. Do not create issues in this phase.

Output a concise structured report with one section per candidate using this exact heading format:

```
Candidate N: <short title>
Current file: <path>
Functions:
- <name>
Why misplaced:
- <reason>
Suggested destination:
- <path or package>
Potential duplicates:
- <path or "none">
```
