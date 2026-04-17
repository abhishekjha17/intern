package profiler

import (
	"encoding/json"
	"io"
)

// RenderJSON writes the profile report as indented JSON to w.
func RenderJSON(w io.Writer, r *ProfileReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
