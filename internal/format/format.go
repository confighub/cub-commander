// Package format renders results as a table (cub-style), JSON or CSV.
package format

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/confighub/cub-commander/internal/exec"
)

func Table(w io.Writer, r *exec.Result, headers bool) {
	widths := make([]int, len(r.Headers))
	cells := make([][]string, len(r.Rows))
	for i, h := range r.Headers {
		widths[i] = len(h)
	}
	for ri, row := range r.Rows {
		cells[ri] = make([]string, len(row))
		for i, v := range row {
			s := exec.Format(v)
			if len(s) > 80 {
				s = s[:77] + "..."
			}
			cells[ri][i] = s
			if len(s) > widths[i] {
				widths[i] = len(s)
			}
		}
	}
	line := func(parts []string) {
		var b strings.Builder
		for i, p := range parts {
			if i > 0 {
				b.WriteString("  ")
			}
			if i == len(parts)-1 {
				b.WriteString(p)
			} else {
				b.WriteString(fmt.Sprintf("%-*s", widths[i], p))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	if headers {
		line(r.Headers)
	}
	for _, c := range cells {
		line(c)
	}
}

func JSON(w io.Writer, r *exec.Result) error {
	out := make([]map[string]any, 0, len(r.Rows))
	for _, row := range r.Rows {
		m := map[string]any{}
		for i, h := range r.Headers {
			m[h] = row[i]
		}
		out = append(out, m)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func CSV(w io.Writer, r *exec.Result) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(r.Headers); err != nil {
		return err
	}
	for _, row := range r.Rows {
		rec := make([]string, len(row))
		for i, v := range row {
			rec[i] = exec.Format(v)
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}
