package visualize

import (
	"encoding/json"
	"io"
)

// RenderJSON writes the graph as indented JSON. This is the intermediate
// format exposed verbatim so other tools can consume it.
func RenderJSON(g *Graph, w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(g)
}
