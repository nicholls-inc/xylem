Turn the file-diet measurement into a semantically meaningful split plan.

Mode: `{{index .Source.Params "mode"}}`
Measurement:

{{index .PreviousOutputs "measure"}}

If the mode is not `file_diet`, output exactly:

```
File-diet split planning skipped for mode {{index .Source.Params "mode"}}.
```

If there is no target file in the measurement output, output exactly:

```
No file-diet split plan proposed.
```

Otherwise, produce one issue draft for the largest offending file using this exact format:

```
Title: [file-diet] <target file>
Target file: <path>
Labels: file-diet, enhancement, ready-for-work
Body:
## Summary
...

## Why this file is oversized
...

## Proposed split
1. ...
2. ...
3. ...

## Suggested new files
- <path>: <responsibility>
- ...

## Acceptance criteria
- ...
```
