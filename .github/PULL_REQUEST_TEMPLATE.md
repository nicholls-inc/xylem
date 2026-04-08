## Summary

-

## Checks

- [ ] `cd cli && goimports -w . && go vet ./... && golangci-lint run && go build ./cmd/xylem && go test ./...`
- [ ] If this PR changes harness surfaces (prompts, model defaults, tool contracts, routing, or policy rules), I ran `xylem eval compare --baseline jobs/baseline --candidate jobs/candidate --fail-on-regression` and included the comparison output or attached report.
