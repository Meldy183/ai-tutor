package xlsx

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func book(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range parts {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const ns = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`

func TestReadReturnsTheCellsOfEverySheet(t *testing.T) {
	data := book(t, map[string]string{
		"xl/workbook.xml": `<workbook ` + ns + `><sheets><sheet name="Карта" sheetId="1" r:id="rId1"/><sheet name="Легенда" sheetId="2" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Target="/xl/worksheets/sheet2.xml"/></Relationships>`,
		"xl/sharedStrings.xml": `<sst ` + ns + `><si><t>Субкомпетенция</t></si><si><r><t>Назовите </t></r><r><t>агента.</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet ` + ns + `><sheetData>` +
			`<row r="1"><c r="B1" t="s"><v>0</v></c></row>` +
			`<row r="3"><c r="A3"><v>4.0</v></c><c r="C3" t="s"><v>1</v></c><c r="D3" t="b"><v>1</v></c><c r="E3" t="inlineStr"><is><t>текст</t></is></c></row>` +
			`</sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml": `<worksheet ` + ns + `><sheetData/></worksheet>`,
	})
	sheets, err := Read(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(sheets) != 2 || sheets[0].Name != "Карта" || sheets[1].Name != "Легенда" || len(sheets[1].Rows) != 0 {
		t.Fatalf("sheets = %+v", sheets)
	}
	rows := sheets[0].Rows
	if len(rows) != 3 || rows[0][1] != "Субкомпетенция" || rows[1] != nil {
		t.Fatalf("rows = %q", rows)
	}
	if got := rows[2]; len(got) != 5 || got[0] != "4.0" || got[1] != "" || got[2] != "Назовите агента." || got[3] != "TRUE" || got[4] != "текст" {
		t.Fatalf("row 3 = %q", got)
	}
}

func TestReadRejectsWhatIsNotABook(t *testing.T) {
	if _, err := Read([]byte("%PDF-1.7")); !errors.Is(err, ErrNotWorkbook) {
		t.Fatalf("pdf: %v", err)
	}
	if _, err := Read(book(t, map[string]string{"word/document.xml": "<document/>"})); !errors.Is(err, ErrNotWorkbook) {
		t.Fatalf("docx: %v", err)
	}
}

func TestReadRefusesALongCellReference(t *testing.T) {
	for _, ref := range []string{"ZZZZZZZZZZZZZZ1", "HZ1", "A", "1A", "A1B"} {
		data := book(t, map[string]string{
			"xl/workbook.xml":            `<workbook ` + ns + `><sheets><sheet name="Карта" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
			"xl/worksheets/sheet1.xml":   `<worksheet ` + ns + `><sheetData><row r="1"><c r="` + ref + `"><v>1</v></c></row></sheetData></worksheet>`,
		})
		if _, err := Read(data); err == nil {
			t.Fatalf("cell %s accepted", ref)
		}
	}
}

func TestReadRefusesABookThatMultipliesItsParts(t *testing.T) {
	var sheets, rels strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&sheets, `<sheet name="Лист %d" sheetId="%d" r:id="rId%d"/>`, i, i, i)
		// Every entry points at the same part.
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Target="worksheets/sheet1.xml"/>`, i)
	}
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook ` + ns + `><sheets>` + sheets.String() + `</sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + rels.String() + `</Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet ` + ns + `><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>` + strings.Repeat("x", 1<<20) + `</t></is></c></row></sheetData></worksheet>`,
	}
	if _, err := Read(book(t, parts)); err == nil || !strings.Contains(err.Error(), "repeats part") {
		t.Fatalf("a part read through many sheets: %v", err)
	}
	sheets.Reset()
	for i := 1; i <= maxSheets+1; i++ {
		fmt.Fprintf(&sheets, `<sheet name="Лист %d" sheetId="%d" r:id="rId1"/>`, i, i)
	}
	parts["xl/workbook.xml"] = `<workbook ` + ns + `><sheets>` + sheets.String() + `</sheets></workbook>`
	if _, err := Read(book(t, parts)); err == nil || !strings.Contains(err.Error(), "sheets") {
		t.Fatalf("too many sheets: %v", err)
	}
}

func TestReadSpendsOneBudgetForTheWholeBook(t *testing.T) {
	r := &reader{slots: 2, text: 6}
	if r.spendText(len("абв")) != nil || r.spendSlots(2) != nil {
		t.Fatal("within the budget refused")
	}
	if r.spendSlots(1) == nil || r.spendText(1) == nil {
		t.Fatal("past the budget accepted")
	}
}

// sheetBook is a book of sheets with the given sheetData XML each.
func sheetBook(t *testing.T, data ...string) []byte {
	t.Helper()
	var sheets, rels strings.Builder
	parts := map[string]string{}
	for i, d := range data {
		fmt.Fprintf(&sheets, `<sheet name="Лист %d" sheetId="%d" r:id="rId%d"/>`, i+1, i+1, i+1)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
		parts[fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)] = `<worksheet ` + ns + `><sheetData>` + d + `</sheetData></worksheet>`
	}
	parts["xl/workbook.xml"] = `<workbook ` + ns + `><sheets>` + sheets.String() + `</sheets></workbook>`
	parts["xl/_rels/workbook.xml.rels"] = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + rels.String() + `</Relationships>`
	return book(t, parts)
}

func TestReadStopsAtTheLimitWhileReading(t *testing.T) {
	// 250 000 empty cells in one row: refused at column 201, while read.
	wide := `<row r="1">` + strings.Repeat(`<c/>`, 250000) + `</row>`
	if _, err := Read(sheetBook(t, wide)); err == nil || !strings.Contains(err.Error(), "column 201") {
		t.Fatalf("a row of 250 000 cells: %v", err)
	}
	// Sparse rows: one empty cell in column 200 per row widens every row to
	// 200 positions; the positions are spent, so the book is refused before
	// the slots of the result exceed maxSlots.
	var sparse strings.Builder
	for i := 1; i <= maxRows; i++ {
		fmt.Fprintf(&sparse, `<row r="%d"><c r="GR%d"/></row>`, i, i)
	}
	if _, err := Read(sheetBook(t, sparse.String())); !errors.Is(err, errTooLarge) {
		t.Fatalf("sparse rows: %v", err)
	}
	// Every element counts, kept or not.
	many := `<row r="1"><c r="A1"><v>1</v></c></row>` + strings.Repeat(`<x/>`, maxElements)
	if _, err := Read(sheetBook(t, many)); !errors.Is(err, errTooLarge) {
		t.Fatalf("elements past the budget: %v", err)
	}
}

func TestReadRefusesAMisplacedElement(t *testing.T) {
	for _, data := range []string{
		`<row r="1"><c r="A1"><row r="2"/></c></row>`,
		`<row r="1"><c r="A1"><row r="2"></row></c></row>`,
		`<row r="1"><row r="2"/></row>`,
		`<c r="A1"><v>1</v></c>`,
		`<row r="1"><c r="A1"><c r="B1"/></c></row>`,
		`<row r="1"><v>1</v></row>`,
		`<row r="1"><c r="A1" t="inlineStr"><t>x</t></c></row>`,
		// The places are checked from the root, not by the parent alone.
		`<row r="1"><c r="A1"><worksheet><sheetData><row r="2"/></sheetData></worksheet></c></row>`,
		`<row r="1"><c r="A1"><sheetData><row r="2"/></sheetData></c></row>`,
		`<row r="1"><c r="A1"><x:w xmlns:x="urn:other"><row r="2"/></x:w></c></row>`,
		`<row r="1"><c r="A1"><extLst><row r="2"><c r="B2"/></row></extLst></c></row>`,
	} {
		if _, err := Read(sheetBook(t, data)); err == nil {
			t.Fatalf("%s accepted", data)
		}
	}
	// An element of another namespace changes nothing, opening or closing.
	other := `<row r="1"><x:row xmlns:x="urn:other" r="5"/><c r="A1"><v>1</v></c></row>`
	if sheets, err := Read(sheetBook(t, other)); err != nil || len(sheets[0].Rows) != 1 || sheets[0].Rows[0][0] != "1" {
		t.Fatalf("foreign element: %v %+v", err, sheets)
	}
}

func TestReadKeepsTheTextOfEveryKindOfCell(t *testing.T) {
	data := sheetBook(t, `<row r="2"><c r="B2" t="inlineStr"><is><r><t>Назовите </t></r><r><t>агента.</t></r></is></c><c r="C2"><v>4.0</v></c></row>`)
	sheets, err := Read(data)
	if err != nil {
		t.Fatal(err)
	}
	if rows := sheets[0].Rows; len(rows) != 2 || rows[0] != nil || len(rows[1]) != 3 || rows[1][1] != "Назовите агента." || rows[1][2] != "4.0" {
		t.Fatalf("rows = %q", rows)
	}
}

// FuzzRead checks that no sheet content makes the reader panic: a bad book
// is an error, never a crash of the request.
func FuzzRead(f *testing.F) {
	for _, seed := range []string{
		`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="inlineStr"><is><r><t>x</t></r></is></c></row>`,
		`<row r="1"><c r="A1"><row r="2"/></c></row>`,
		`<row r="2"><c r="GR2"/></row><row r="3"><c t="b"><v>1</v></c></row>`,
		`<row r="1"><c r="A1"><worksheet><sheetData><row r="2"/></sheetData></worksheet></c></row>`,
		`</sheetData><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData><sheetData>`,
		`<row r="1"><c r="A1" t="inlineStr"><is><rPh><t>ふ</t></rPh><r><t>x</t></r></is></c></row>`,
	} {
		f.Add(`<worksheet ` + ns + `><sheetData>` + seed + `</sheetData></worksheet>`)
	}
	f.Add(`<worksheet ` + ns + `><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData><worksheet><sheetData><row r="2"/></sheetData></worksheet></worksheet>`)
	f.Fuzz(func(t *testing.T, data string) {
		parts := map[string]string{
			"xl/workbook.xml":            `<workbook ` + ns + `><sheets><sheet name="Карта" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
			"xl/sharedStrings.xml":       `<sst ` + ns + `><si><t>Субкомпетенция</t></si></sst>`,
			// The whole part is fuzzed, root and sheetData included.
			"xl/worksheets/sheet1.xml": data,
		}
		_, _ = Read(book(t, parts))
	})
}

// A competency map is its first two sheets: whatever the later sheets hold,
// even a cell past the column limit or more rows than the budget, does not
// stop the import. A failure inside the two map sheets still does.
func TestReadStopsAfterTheTwoMapSheets(t *testing.T) {
	first := `<row r="1"><c r="A1" t="inlineStr"><is><t>Обучение</t></is></c></row>`
	second := `<row r="1"><c r="A1" t="inlineStr"><is><t>Карта</t></is></c></row>`
	huge := strings.Repeat(`<row><c><v>1</v></c></row>`, maxRows+1)
	for name, third := range map[string]string{"column out of range": `<row r="1"><c r="GS1"><v>1</v></c></row>`, "rows over the budget": huge} {
		sheets, err := Read(sheetBook(t, first, second, third))
		if err != nil || len(sheets) != 2 {
			t.Fatalf("%s: sheets = %+v, %v", name, sheets, err)
		}
		if sheets[0].Name != "Лист 1" || sheets[0].Rows[0][0] != "Обучение" || sheets[1].Name != "Лист 2" || sheets[1].Rows[0][0] != "Карта" {
			t.Fatalf("%s: sheets = %+v", name, sheets)
		}
	}
	bad := `<row r="1"><c r="GS1"><v>1</v></c></row>`
	if _, err := Read(sheetBook(t, first, bad)); err == nil || !strings.Contains(err.Error(), "Лист 2") {
		t.Fatalf("an error in the second sheet: %v", err)
	}
}
