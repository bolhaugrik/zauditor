package report

import (
	"encoding/json"
	"io"

	"zauditor/internal/score"
)

// JSON writes the machine-readable report. The shape is the score.Report
// struct itself, so a new dimension needs no renderer change. Keys are stable;
// treat them as the CI contract.
func JSON(w io.Writer, rep score.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		score.Report
		SchemaVersion int `json:"schema_version"`
	}{Report: rep, SchemaVersion: 1})
}
