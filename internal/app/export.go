package app

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"sastoolkit/internal/val"
)

// Column is one (key, label) pair of an export.
type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// exportValue is the value a column shows, for a file. Mirrors the table's
// computed columns so an export reads the same as the screen.
func exportValue(key string, user M, now time.Time) any {
	switch key {
	case "fullname":
		parts := []string{}
		for _, k := range []string{"firstname", "lastname"} {
			if s := val.Str(user[k]); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	case "status":
		return strings.Join(statusOf(user, now).sorted(), ", ")
	}
	if v, ok := user[key]; ok && v != nil {
		return v
	}
	return ""
}

// ExportCSV writes UTF-8 with a BOM so Excel shows Arabic names correctly.
func ExportCSV(w io.Writer, rows []M, columns []Column) error {
	now := time.Now()
	buf := bufio.NewWriterSize(w, 64<<10)
	buf.WriteString("\ufeff")
	writer := csv.NewWriter(buf)
	writer.UseCRLF = true
	head := make([]string, len(columns))
	for i, c := range columns {
		head[i] = c.Label
	}
	writer.Write(head)
	line := make([]string, len(columns))
	for _, row := range rows {
		for i, c := range columns {
			line[i] = val.Str(exportValue(c.Key, row, now))
		}
		writer.Write(line)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	return buf.Flush()
}

// xlsxCell keeps text as text -- Excel would read '0912345678' as a number
// and drop the zero -- and only hands over real numbers as numbers.
func xlsxCell(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int, int64, float64:
		return x
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
	}
	return val.Str(v)
}

// writeSheet streams rows into a one-sheet workbook.
func writeSheet(name string, rows [][]any) ([]byte, error) {
	book := excelize.NewFile()
	defer book.Close()
	if err := book.SetSheetName("Sheet1", name); err != nil {
		return nil, err
	}
	sw, err := book.NewStreamWriter(name)
	if err != nil {
		return nil, err
	}
	for i, row := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := sw.SetRow(cell, row); err != nil {
			return nil, err
		}
	}
	if err := sw.Flush(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := book.Write(&out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func ExportXLSX(rows []M, columns []Column) ([]byte, error) {
	now := time.Now()
	head := make([]any, len(columns))
	for i, c := range columns {
		head[i] = c.Label
	}
	lines := make([][]any, 0, len(rows)+1)
	lines = append(lines, head)
	for _, row := range rows {
		line := make([]any, len(columns))
		for i, c := range columns {
			line[i] = xlsxCell(exportValue(c.Key, row, now))
		}
		lines = append(lines, line)
	}
	return writeSheet("Users", lines)
}
