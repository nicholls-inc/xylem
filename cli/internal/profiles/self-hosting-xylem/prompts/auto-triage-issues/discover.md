Prepare the next batch of unlabeled GitHub issues for automated triage.

Repository: {{.Repo.Slug}}

Your job in this phase is discovery only. Do not edit issues or labels yet.

1. List the repository labels so later phases can validate against the real taxonomy:

   `gh label list --repo {{.Repo.Slug}} --limit 200 --json name,description,color`

2. List open issues and keep only issues that currently have zero labels:

   `gh issue list --repo {{.Repo.Slug}} --state open --limit 100 --json number,title,body,url,author,labels,createdAt,updatedAt`

3. Sort the unlabeled issues by `updatedAt` ascending so older untouched issues are handled first.
4. Keep at most 10 issues for this run.
5. For each selected issue, fetch `authorAssociation` separately with GraphQL. `gh issue list --json` does not expose author association, so do not guess it. Use a query in this shape, substituting the owner/name from `{{.Repo.Slug}}` and the issue number:

   `gh api graphql -f query='query($owner:String!, $repo:String!, $number:Int!) { repository(owner:$owner, name:$repo) { issue(number:$number) { authorAssociation } } }' -F owner=<owner> -F repo=<repo> -F number=<number>`

6. For each selected issue, capture:
   - `number`
   - `title`
   - `url`
   - `author_login`
   - `author_association`
   - `created_at`
   - `updated_at`
   - `body_excerpt` — concise excerpt or summary of the problem statement, reproduction steps, and requested change. Keep it short enough for later phases.

If there are no open unlabeled issues, output a standalone line containing `XYLEM_NOOP`, then a one-sentence explanation.

Otherwise output JSON only, with no code fences or prose before/after it, using this shape:

```json
{
  "repo": "{{.Repo.Slug}}",
  "batch_limit": 10,
  "repo_labels": [
    {"name": "bug", "description": "", "color": "d73a4a"}
  ],
  "issues": [
    {
      "number": 123,
      "title": "Example",
      "url": "https://github.com/owner/repo/issues/123",
      "author_login": "octocat",
      "author_association": "NONE",
      "created_at": "2026-04-10T00:00:00Z",
      "updated_at": "2026-04-10T01:00:00Z",
      "body_excerpt": "Short summary of the issue body."
    }
  ]
}
```
