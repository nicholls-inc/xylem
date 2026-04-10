#!/usr/bin/env bash
set -euo pipefail

# post-discussion.sh — Create or comment on a GitHub Discussion via GraphQL.
#
# Usage:
#   post-discussion.sh --owner OWNER --repo NAME --category CATEGORY_NAME \
#                      --title TITLE --body BODY [--title-search PREFIX]
#
# If an open discussion whose title starts with --title-search (or --title if
# --title-search is omitted) already exists in the given category, a comment is
# appended. Otherwise a new discussion is created.
#
# Prints the discussion URL to stdout.

OWNER=""
REPO=""
CATEGORY=""
TITLE=""
BODY=""
TITLE_SEARCH=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --owner)        OWNER="$2";        shift 2 ;;
    --repo)         REPO="$2";         shift 2 ;;
    --category)     CATEGORY="$2";     shift 2 ;;
    --title)        TITLE="$2";        shift 2 ;;
    --body)         BODY="$2";         shift 2 ;;
    --title-search) TITLE_SEARCH="$2"; shift 2 ;;
    *) echo "Unknown flag: $1" >&2; exit 1 ;;
  esac
done

if [[ -z "$OWNER" || -z "$REPO" || -z "$CATEGORY" || -z "$TITLE" || -z "$BODY" ]]; then
  echo "Usage: post-discussion.sh --owner OWNER --repo NAME --category CATEGORY_NAME --title TITLE --body BODY [--title-search PREFIX]" >&2
  exit 1
fi

if [[ -z "$TITLE_SEARCH" ]]; then
  TITLE_SEARCH="$TITLE"
fi

# Resolve the repository node ID and discussion category ID by name.
RESOLVE_QUERY='query($owner: String!, $repo: String!) {
  repository(owner: $owner, name: $repo) {
    id
    discussionCategories(first: 25) {
      nodes { id, name }
    }
  }
}'

RESOLVE_RESULT=$(gh api graphql \
  -f query="$RESOLVE_QUERY" \
  -f owner="$OWNER" \
  -f repo="$REPO")

REPO_ID=$(echo "$RESOLVE_RESULT" | jq -r '.data.repository.id')
CATEGORY_ID=$(echo "$RESOLVE_RESULT" | jq -r --arg cat "$CATEGORY" \
  '.data.repository.discussionCategories.nodes[] | select(.name == $cat) | .id')

if [[ -z "$REPO_ID" || "$REPO_ID" == "null" ]]; then
  echo "Error: could not resolve repository ${OWNER}/${REPO}" >&2
  exit 1
fi
if [[ -z "$CATEGORY_ID" || "$CATEGORY_ID" == "null" ]]; then
  echo "Error: discussion category '${CATEGORY}' not found in ${OWNER}/${REPO}" >&2
  exit 1
fi

# Search for an existing discussion in the category whose title starts with the
# search prefix. We fetch the 20 most recent and match client-side because the
# GraphQL discussions connection does not support full-text title filtering.
SEARCH_QUERY='query($repoId: ID!, $catId: ID!) {
  node(id: $repoId) {
    ... on Repository {
      discussions(first: 20, categoryId: $catId, orderBy: {field: CREATED_AT, direction: DESC}) {
        nodes { id, title, url }
      }
    }
  }
}'

SEARCH_RESULT=$(gh api graphql \
  -f query="$SEARCH_QUERY" \
  -f repoId="$REPO_ID" \
  -f catId="$CATEGORY_ID")

# Extract matching discussion by title prefix.
DISCUSSION_ID=""
DISCUSSION_URL=""

if echo "$SEARCH_RESULT" | jq -e '.data.node.discussions.nodes' >/dev/null 2>&1; then
  MATCH=$(echo "$SEARCH_RESULT" | jq -r --arg prefix "$TITLE_SEARCH" '
    .data.node.discussions.nodes[]
    | select(.title | startswith($prefix))
    | {id, url}' | jq -s 'first // empty')

  if [[ -n "$MATCH" && "$MATCH" != "null" ]]; then
    DISCUSSION_ID=$(echo "$MATCH" | jq -r '.id')
    DISCUSSION_URL=$(echo "$MATCH" | jq -r '.url')
  fi
fi

if [[ -n "$DISCUSSION_ID" ]]; then
  # Append a comment to the existing discussion.
  COMMENT_MUTATION='mutation($discussionId: ID!, $body: String!) {
    addDiscussionComment(input: {discussionId: $discussionId, body: $body}) {
      comment { url }
    }
  }'

  gh api graphql \
    -f query="$COMMENT_MUTATION" \
    -f discussionId="$DISCUSSION_ID" \
    -f body="$BODY" >/dev/null

  echo "$DISCUSSION_URL"
else
  # Create a new discussion.
  CREATE_MUTATION='mutation($repoId: ID!, $catId: ID!, $title: String!, $body: String!) {
    createDiscussion(input: {repositoryId: $repoId, title: $title, body: $body, categoryId: $catId}) {
      discussion { id, url }
    }
  }'

  CREATE_RESULT=$(gh api graphql \
    -f query="$CREATE_MUTATION" \
    -f repoId="$REPO_ID" \
    -f catId="$CATEGORY_ID" \
    -f title="$TITLE" \
    -f body="$BODY")

  echo "$CREATE_RESULT" | jq -r '.data.createDiscussion.discussion.url'
fi
