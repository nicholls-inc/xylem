// retry_dag.dfy — Dafny spec for the retry-DAG acyclicity kernel.
// Source of truth for cli/internal/queue/verified/retry_dag.go.
//
// The retry graph is a functional graph (out-degree ≤ 1) represented as
// map<string, string> mapping vessel ID → RetryOf target.
// Only entries where the value is non-empty represent retry edges.
//
// Verified by: Dafny 4.11.0 (mcp__plugin_crosscheck_dafny__dafny_verify: 2 verified, 0 errors)
// Extracted to: retry_dag.go via crosscheck:extract-code
//
// To re-verify: run mcp__plugin_crosscheck_dafny__dafny_verify on this file.
// To re-extract: run the crosscheck:extract-code skill targeting Go.
//
// Scope: PathExists (bounded reachability) + IsAcyclic (cycle detection).

// PathExists returns true iff there is a directed path from `start` to `target`
// in at most `fuel` hops through the edge map.
//
// Termination measure: fuel strictly decreases on each recursive call.
// Base cases: fuel == 0 (depth exhausted) or start not in edges (no outgoing edge).
function PathExists(edges: map<string, string>, start: string, target: string, fuel: nat): bool
  decreases fuel
{
  if fuel == 0 then false
  else if start !in edges then false
  else edges[start] == target || PathExists(edges, edges[start], target, fuel - 1)
}

// IsAcyclic returns true iff the retry graph contains no directed cycle.
//
// Formally: for every vessel id that has a RetryOf entry, there is no directed
// path from edges[id] back to id within |edges| hops. Since the retry graph has
// out-degree ≤ 1, any cycle has length ≤ |edges| (the number of nodes with
// outgoing edges), so |edges| is a sound upper bound on cycle length.
function IsAcyclic(edges: map<string, string>): bool
  ensures IsAcyclic(edges) <==>
    (forall id :: id in edges ==> !PathExists(edges, edges[id], id, |edges|))
{
  forall id :: id in edges ==> !PathExists(edges, edges[id], id, |edges|)
}
