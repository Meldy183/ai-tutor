// Package xlsx reads the two map worksheets of an Excel book as rows of cell
// text; later sheets are never read. It needs only the standard library: a
// competency map is plain text cells.
package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/coursegraph"
)

// Limits guard against a crafted book that unpacks into far more than it
// weighs. They bound the whole book, not one part, and are checked token by
// token while the XML is read, before anything grows: every XML part read
// spends one byte budget, every element one element budget, and every row
// and cell position held in the result (empty positions of a sparse row
// included) one slot budget.
const (
	maxBookBytes = 64 << 20
	maxSheets    = 64
	maxRows      = 20000
	maxColumns   = 200
	// maxElements bounds the XML elements of the book, empty ones included.
	maxElements = 2_000_000
	// maxSlots bounds the rows and cell positions returned, so a sparse sheet
	// cannot widen into dense rows.
	maxSlots = 1_000_000
	// maxTextBytes bounds the text read from the book and returned, shared
	// strings counted at every cell that refers to them.
	maxTextBytes = 64 << 20
	// maxRelationships bounds the relationships of the workbook part.
	maxRelationships = 4 * maxSheets
)

// ErrNotWorkbook means the file is not an .xlsx book.
var ErrNotWorkbook = errors.New("not an xlsx workbook")

var (
	errTooLarge      = errors.New("the book is larger than a competency map may be")
	errTooManySheets = fmt.Errorf("the book has more than %d sheets", maxSheets)
)

const (
	mainNS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	relNS  = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
)

type reader struct {
	files map[string]*zip.File
	// What the book may still unpack, contain and return.
	bytes, elements, slots, text int64
}

type sheetRef struct{ name, id string }

// mapSheets is the number of leading worksheets that make up a competency
// map. Later sheets (reference data and the like) are never read, so they
// can neither fail the import nor spend its budget. The shared strings are
// common to the book and are still read whole.
const mapSheets = 2

// Read returns the first mapSheets worksheets of the book, in book order.
func Read(data []byte) ([]coursegraph.MapSheet, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, ErrNotWorkbook
	}
	r := &reader{files: map[string]*zip.File{}, bytes: maxBookBytes, elements: maxElements, slots: maxSlots, text: maxTextBytes}
	for _, f := range archive.File {
		r.files[f.Name] = f
	}
	refs, err := r.workbook()
	if err != nil {
		return nil, err
	}
	if len(refs) > mapSheets {
		refs = refs[:mapSheets]
	}
	targets, err := r.relationships()
	if err != nil {
		return nil, err
	}
	shared, err := r.sharedStrings()
	if err != nil {
		return nil, err
	}
	sheets := make([]coursegraph.MapSheet, 0, len(refs))
	read := map[string]bool{}
	for _, s := range refs {
		target, ok := targets[s.id]
		if !ok {
			continue
		}
		// A sheet part belongs to one sheet; reading it again would only
		// multiply what the book returns.
		if read[target] {
			return nil, fmt.Errorf("sheet %q repeats part %s", s.name, target)
		}
		read[target] = true
		rows, err := r.rows(target, shared)
		if err != nil {
			return nil, fmt.Errorf("sheet %q: %w", s.name, err)
		}
		sheets = append(sheets, coursegraph.MapSheet{Name: s.name, Rows: rows})
	}
	return sheets, nil
}

// walk streams a part, spending the budgets as it goes, and calls start for
// every element, end for every closing tag and text for character data.
func (r *reader) walk(name string, start func(xml.StartElement) error, end func(xml.EndElement) error, text func([]byte) error) error {
	f, ok := r.files[name]
	if !ok {
		return fmt.Errorf("%s is missing", name)
	}
	// The declared size is spent at once and also bounds the reading, so an
	// archive that understates it gains nothing.
	if f.UncompressedSize64 > uint64(r.bytes) {
		return errTooLarge
	}
	size := int64(f.UncompressedSize64)
	r.bytes -= size
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	dec := xml.NewDecoder(io.LimitReader(rc, size))
	for {
		token, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if r.elements--; r.elements < 0 {
				return errTooLarge
			}
			if start != nil {
				if err := start(t); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if end != nil {
				if err := end(t); err != nil {
					return err
				}
			}
		case xml.CharData:
			if text != nil {
				if err := text(t); err != nil {
					return err
				}
			}
		}
	}
}

func attr(e xml.StartElement, space, local string) string {
	for _, a := range e.Attr {
		if a.Name.Local == local && (space == "" || a.Name.Space == space) {
			return a.Value
		}
	}
	return ""
}

func (r *reader) workbook() ([]sheetRef, error) {
	refs := []sheetRef{}
	err := r.walk("xl/workbook.xml", func(e xml.StartElement) error {
		if e.Name.Local != "sheet" {
			return nil
		}
		if len(refs) == maxSheets {
			return errTooManySheets
		}
		refs = append(refs, sheetRef{name: attr(e, "", "name"), id: attr(e, relNS, "id")})
		return nil
	}, nil, nil)
	if errors.Is(err, errTooLarge) || errors.Is(err, errTooManySheets) {
		return nil, err
	}
	if err != nil {
		return nil, ErrNotWorkbook
	}
	return refs, nil
}

func (r *reader) relationships() (map[string]string, error) {
	targets := map[string]string{}
	err := r.walk("xl/_rels/workbook.xml.rels", func(e xml.StartElement) error {
		if e.Name.Local != "Relationship" {
			return nil
		}
		if len(targets) == maxRelationships {
			return errTooLarge
		}
		target := strings.TrimPrefix(attr(e, "", "Target"), "/")
		if !strings.HasPrefix(target, "xl/") {
			target = path.Join("xl", target)
		}
		targets[attr(e, "", "Id")] = target
		return nil
	}, nil, nil)
	if errors.Is(err, errTooLarge) {
		return nil, err
	}
	if err != nil {
		return nil, ErrNotWorkbook
	}
	return targets, nil
}

// spendText takes text from the budget of the book.
func (r *reader) spendText(n int) error {
	if r.text -= int64(n); r.text < 0 {
		return errTooLarge
	}
	return nil
}

// spendSlots takes rows or cell positions from the budget of the book.
func (r *reader) spendSlots(n int) error {
	if r.slots -= int64(n); r.slots < 0 {
		return errTooLarge
	}
	return nil
}

// sharedStrings reads the string table: the text of each item, rich text
// runs joined, phonetic hints left out.
func (r *reader) sharedStrings() ([]string, error) {
	if _, ok := r.files["xl/sharedStrings.xml"]; !ok {
		return nil, nil
	}
	var (
		out     []string
		current strings.Builder
		inItem  bool
		inText  bool
		// Phonetic runs (rPh) carry readings, not the cell text.
		phonetic int
	)
	err := r.walk("xl/sharedStrings.xml", func(e xml.StartElement) error {
		switch e.Name.Local {
		case "si":
			if err := r.spendSlots(1); err != nil {
				return err
			}
			inItem = true
			current.Reset()
		case "rPh":
			phonetic++
		case "t":
			inText = inItem && phonetic == 0
		}
		return nil
	}, func(e xml.EndElement) error {
		switch e.Name.Local {
		case "si":
			out = append(out, current.String())
			inItem = false
		case "rPh":
			phonetic--
		case "t":
			inText = false
		}
		return nil
	}, func(data []byte) error {
		if !inText {
			return nil
		}
		if err := r.spendText(len(data)); err != nil {
			return err
		}
		current.Write(data)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("shared strings: %w", err)
	}
	return out, nil
}

// rows reads a worksheet. Each row and cell position is spent before the
// row grows to it, so a sparse sheet is bounded like a dense one.
func (r *reader) rows(name string, shared []string) ([][]string, error) {
	rows := [][]string{}
	var (
		row    []string
		inRow  bool
		next   int // column of a cell without a reference
		column = -1
		kind   string
		value  strings.Builder
		inV    bool
		inT    bool
		// open holds the role of every element now open, any namespace, and
		// unknown for elements without one. An element takes a role only at
		// its one place in the document, and a closing tag acts by the role
		// of what it closes, never by its name, so no element can open a row
		// or a cell inside another one.
		open []string
	)
	const unknown = "?"
	// places are where the elements that change the state may stand, as
	// the roles from the root down. t and r inside an element without a
	// role (phonetic hints in rPh) are read through.
	places := map[string][]string{
		"worksheet": {""},
		"sheetData": {"worksheet"},
		"row":       {"worksheet/sheetData"},
		"c":         {"worksheet/sheetData/row"},
		"v":         {"worksheet/sheetData/row/c"},
		"is":        {"worksheet/sheetData/row/c"},
		"r":         {"worksheet/sheetData/row/c/is"},
		"t":         {"worksheet/sheetData/row/c/is", "worksheet/sheetData/row/c/is/r"},
	}
	// setCell stores the value read for the current cell; the text of the
	// file was spent while read, a shared string is spent at every cell.
	setCell := func() error {
		cell := value.String()
		switch kind {
		case "s":
			index, err := strconv.Atoi(strings.TrimSpace(cell))
			if err != nil || index < 0 || index >= len(shared) {
				return fmt.Errorf("a cell refers to an unknown string")
			}
			cell = shared[index]
			if err := r.spendText(len(cell)); err != nil {
				return err
			}
		case "b":
			cell = map[string]string{"1": "TRUE", "0": "FALSE"}[cell]
		}
		row[column] = cell
		return nil
	}
	err := r.walk(name, func(e xml.StartElement) error {
		role := unknown
		if allowed, ok := places[e.Name.Local]; ok && (e.Name.Space == "" || e.Name.Space == mainNS) {
			switch {
			case slices.Contains(allowed, strings.Join(open, "/")):
				role = e.Name.Local
			case (e.Name.Local == "t" || e.Name.Local == "r") && slices.Contains(open, unknown):
			default:
				return fmt.Errorf("<%s> out of place in the sheet", e.Name.Local)
			}
		}
		open = append(open, role)
		switch role {
		case "row":
			number := len(rows) + 1
			if ref := attr(e, "", "r"); ref != "" {
				n, err := strconv.Atoi(ref)
				if err != nil || n < number {
					return fmt.Errorf("row %q is out of order", ref)
				}
				number = n
			}
			if number > maxRows {
				return fmt.Errorf("row %d is out of range", number)
			}
			// Missing rows are empty; each one is a slot of the result.
			if err := r.spendSlots(number - len(rows)); err != nil {
				return err
			}
			for len(rows) < number-1 {
				rows = append(rows, nil)
			}
			row, inRow, next = nil, true, 0
		case "c":
			if !inRow {
				return fmt.Errorf("a cell outside a row")
			}
			column = next
			if ref := attr(e, "", "r"); ref != "" {
				var err error
				if column, err = columnIndex(ref); err != nil {
					return err
				}
			}
			if column < 0 || column >= maxColumns {
				return fmt.Errorf("column %d is out of range", column+1)
			}
			if column < len(row) && row[column] != "" {
				return fmt.Errorf("cell in column %d repeats", column+1)
			}
			if column >= len(row) {
				if err := r.spendSlots(column + 1 - len(row)); err != nil {
					return err
				}
				row = append(row, make([]string, column+1-len(row))...)
			}
			next = column + 1
			kind = attr(e, "", "t")
			value.Reset()
		case "v":
			inV = true
		case "t":
			inT = true
		}
		return nil
	}, func(xml.EndElement) error {
		if len(open) == 0 {
			return fmt.Errorf("a closing tag without an element")
		}
		role := open[len(open)-1]
		open = open[:len(open)-1]
		switch role {
		case "v":
			inV = false
		case "t":
			inT = false
		case "c":
			if column < 0 {
				return nil
			}
			err := setCell()
			column = -1
			return err
		case "row":
			if inRow {
				rows = append(rows, row)
			}
			inRow = false
		}
		return nil
	}, func(data []byte) error {
		if column < 0 || !inV && !inT {
			return nil
		}
		if err := r.spendText(len(data)); err != nil {
			return err
		}
		value.Write(data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// columnIndex reads the column of a cell reference such as "AB12": letters
// then a row number. A column past maxColumns is refused while the letters
// are read, before the index can overflow.
func columnIndex(ref string) (int, error) {
	n, i := 0, 0
	for ; i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z'; i++ {
		n = n*26 + int(ref[i]-'A'+1)
		if n > maxColumns {
			return 0, fmt.Errorf("column of cell %.16q is out of range", ref)
		}
	}
	digits := ref[i:]
	if n == 0 || digits == "" || strings.Trim(digits, "0123456789") != "" {
		return 0, fmt.Errorf("bad cell reference %.16q", ref)
	}
	return n - 1, nil
}
