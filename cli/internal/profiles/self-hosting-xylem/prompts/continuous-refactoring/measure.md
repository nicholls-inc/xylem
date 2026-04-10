Prepare the file-diet path for the recurring continuous-refactoring workflow.

Mode: `{{index .Source.Params "mode"}}`
Source dirs: `{{index .Source.Params "source_dirs"}}`
File extensions: `{{index .Source.Params "file_extensions"}}`
Exclude patterns: `{{index .Source.Params "exclude_patterns"}}`
LOC threshold: `{{index .Source.Params "loc_threshold"}}`

If the mode is not `file_diet`, output exactly:

```
File-diet measurement skipped for mode {{index .Source.Params "mode"}}.
```

If the mode is `file_diet`:

1. Measure candidate source files in the configured directories.
2. Ignore generated/test fixtures and excluded patterns.
3. Find the single largest file over the configured LOC threshold.
4. If no file exceeds the threshold, say so clearly.

If a target exists, start your output with:

```
Target file: <path>
Target LOC: <count>
```

Then add:

```
Top offenders:
- <path> (<loc>)
- ...

Why this file is a good file-diet candidate:
- ...
```
