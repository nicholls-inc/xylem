// Derived from retry_dag.dfy; DO NOT EDIT by hand.
// Verified by Dafny 4.11.0 — 2 verified, 0 errors.
// To regenerate: compile retry_dag.dfy to Go, then strip _dafny.* boilerplate
// and map Dafny map<string,string>/nat to Go map[string]string/int per the type
// mapping table in README.md.
//
// The verified postcondition is preserved as a doc-comment on each function.
package verified

// PathExists reports whether there is a directed path from start to target
// within fuel hops in the retry edge graph.
//
// Verified postcondition:
//
//	PathExists(edges, start, target, fuel) <==>
//	  there exists a chain start → v1 → ... → target of length ≤ fuel
//	  following edges in the map.
//
// Dafny source: PathExists(edges: map<string,string>, start, target: string, fuel: nat): bool
// in retry_dag.dfy.
// Termination: fuel decreases by 1 on each recursive call; base cases are fuel==0
// (depth exhausted) and start not in edges (no outgoing edge).
func PathExists(edges map[string]string, start, target string, fuel int) bool {
	if fuel == 0 {
		return false
	}
	next, ok := edges[start]
	if !ok {
		return false
	}
	return next == target || PathExists(edges, next, target, fuel-1)
}

// IsAcyclic reports whether the retry graph contains no directed cycle.
//
// Verified postcondition:
//
//	IsAcyclic(edges) <==>
//	  forall id in edges: !PathExists(edges, edges[id], id, len(edges))
//
// Since the retry graph has out-degree ≤ 1 (each vessel has at most one RetryOf
// parent), any cycle has length ≤ len(edges), so len(edges) is a sound fuel bound.
//
// Dafny source: IsAcyclic(edges: map<string,string>): bool in retry_dag.dfy.
// Caller contract: edges maps vessel ID → RetryOf target for all vessels that
// have a non-empty RetryOf field. Vessels with no RetryOf must not appear as keys.
func IsAcyclic(edges map[string]string) bool {
	for id, next := range edges {
		if PathExists(edges, next, id, len(edges)) {
			return false
		}
	}
	return true
}
