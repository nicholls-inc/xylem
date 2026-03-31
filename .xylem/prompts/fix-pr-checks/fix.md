Fix the failing CI checks identified in the diagnosis.

Issue: {{.Issue.Title}}
URL: {{.Issue.URL}}

## Diagnosis
{{.PreviousOutputs.diagnose}}

{{if .GateResult}}
## Previous Gate Failure
The following gate check failed after the previous attempt. Fix the issues and try again:

{{.GateResult}}
{{end}}

## Fix procedure

Work through the failures identified in the diagnosis. Apply fixes in this order:

### 1. Fix formatting

Run `cd cli && goimports -w .` to fix all import ordering and formatting issues.

### 2. Fix compilation errors

Run `cd cli && go vet ./...` and `cd cli && go build ./cmd/xylem` to surface any remaining compilation or static analysis errors. Fix each one.

### 3. Fix failing tests

Run `cd cli && go test ./...` to identify failing tests. Read each failing test to understand what it expects, then fix the code or test as appropriate.

### 4. Verify full CI locally

Run the complete CI gate command to confirm everything passes:

```
cd cli && goimports -l . | grep -c . | xargs test 0 -eq && go vet ./... && go build ./cmd/xylem && go test ./...
```

If any step still fails, fix the issue and re-run until the full command succeeds.
