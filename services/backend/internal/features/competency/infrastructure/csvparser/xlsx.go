package csvparser

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/competencymap"
)

const maxWorksheetRows = 10000
const maxWorksheetColumns = 100

// Escape cell line breaks before the CSV bridge so worksheet row numbers stay physical.
var escapeXLSXCell = strings.NewReplacer("\\", "\\\\", "\r", "\\r", "\n", "\\n")
var restoreXLSXCell = strings.NewReplacer("\\\\", "\\", "\\r", "\r", "\\n", "\n")

func parseXLSX(data []byte) (competencymap.Map, error) {
	return parseXLSXSheet(data, -1)
}

// ParseXLSXSheet parses the requested worksheet of an XLSX competency map.
// The existing Parse API keeps its established convention: a multi-sheet
// workbook's second sheet is the learning map, while a one-sheet workbook
// uses its first sheet. Callers that coordinate the workbook's multiple maps
// can select a sheet explicitly with a zero-based index.
func ParseXLSXSheet(data []byte, sheetIndex int) (competencymap.Map, error) {
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return competencymap.Map{}, csvError(1, "XLSX", "Ожидается книга XLSX.")
	}
	return parseXLSXSheet(data, sheetIndex)
}

// XLSXSheetCount returns the number of worksheets without parsing map rows.
func XLSXSheetCount(data []byte) (int, error) {
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return 0, csvError(1, "XLSX", "Ожидается книга XLSX.")
	}
	workbook, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{
		RawCellValue: true, UnzipSizeLimit: 128 << 20, UnzipXMLSizeLimit: 16 << 20,
	})
	if err != nil {
		return 0, csvError(1, "XLSX", "Не удалось прочитать книгу Excel.")
	}
	defer workbook.Close()
	return len(workbook.GetSheetList()), nil
}

func parseXLSXSheet(data []byte, sheetIndex int) (competencymap.Map, error) {
	workbook, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{
		RawCellValue: true, UnzipSizeLimit: 128 << 20, UnzipXMLSizeLimit: 16 << 20,
	})
	if err != nil {
		return competencymap.Map{}, csvError(1, "XLSX", "Не удалось прочитать книгу Excel.")
	}
	defer workbook.Close()

	sheets := workbook.GetSheetList()
	if len(sheets) == 0 {
		return competencymap.Map{}, csvError(1, "XLSX", "В книге нет листов.")
	}
	if sheetIndex < 0 {
		sheetIndex = 0
		if len(sheets) > 1 {
			sheetIndex = 1
		}
	}
	if sheetIndex >= len(sheets) {
		return competencymap.Map{}, csvError(1, "XLSX", fmt.Sprintf("В книге нет листа с номером %d.", sheetIndex+1))
	}
	sheet := sheets[sheetIndex]
	selected, err := worksheetRows(workbook, sheet)
	if err != nil {
		return competencymap.Map{}, csvError(1, "XLSX", "Размер листа превышает предел или лист повреждён.")
	}
	selectedHeader := headerIndex(selected)
	if selectedHeader < 0 {
		message := "Не найдены заголовки карты компетенций на листе «" + sheet + "»."
		if len(sheets) > 1 {
			message = "Не найдены заголовки карты компетенций на втором листе «" + sheet + "»: диагностика читает второй лист книги, первый — для симуляции экзамена."
		}
		return competencymap.Map{}, csvError(1, "XLSX", message)
	}
	return parseRows(selected, selectedHeader)
}

// ParseSheetRows parses worksheet rows already read from a book, cell text as
// is: a sheet projected from a unified map has no worksheet of its own.
func ParseSheetRows(rows [][]string) (competencymap.Map, error) {
	if len(rows) > maxWorksheetRows {
		return competencymap.Map{}, csvError(1, "XLSX", "Размер листа превышает предел.")
	}
	width := 0
	for _, row := range rows {
		width = max(width, len(row))
	}
	if width > maxWorksheetColumns {
		return competencymap.Map{}, csvError(1, "XLSX", "Размер листа превышает предел.")
	}
	escaped := make([][]string, len(rows))
	for i, row := range rows {
		escaped[i] = make([]string, width)
		for j, value := range row {
			escaped[i][j] = escapeXLSXCell.Replace(value)
		}
	}
	header := headerIndex(escaped)
	if header < 0 {
		return competencymap.Map{}, csvError(1, "XLSX", "Не найдены заголовки карты компетенций.")
	}
	return parseRows(escaped, header)
}

// parseRows bridges escaped worksheet rows to the CSV reader; the rows above
// the header stay as empty lines so row numbers remain those of the sheet.
func parseRows(selected [][]string, selectedHeader int) (competencymap.Map, error) {
	var encoded bytes.Buffer
	writer := csv.NewWriter(&encoded)
	for range selected[:selectedHeader] {
		if err := writer.Write(make([]string, len(selected[selectedHeader]))); err != nil {
			return competencymap.Map{}, err
		}
	}
	for _, row := range selected[selectedHeader:] {
		if err := writer.Write(row); err != nil {
			return competencymap.Map{}, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return competencymap.Map{}, err
	}
	return parseCSV(encoded.Bytes(), restoreXLSXCell.Replace)
}

func worksheetRows(workbook *excelize.File, sheet string) ([][]string, error) {
	rows, err := workbook.GetRows(sheet, excelize.Options{RawCellValue: true})
	if err != nil {
		return nil, err
	}
	if len(rows) > maxWorksheetRows {
		return nil, io.ErrShortBuffer
	}
	lastColumn := 0
	for _, row := range rows {
		lastColumn = max(lastColumn, len(row))
	}
	if lastColumn > maxWorksheetColumns {
		return nil, io.ErrShortBuffer
	}
	for i := range rows {
		if len(rows[i]) < lastColumn {
			rows[i] = append(rows[i], make([]string, lastColumn-len(rows[i]))...)
		}
		for j := range rows[i] {
			cell, err := excelize.CoordinatesToCellName(j+1, i+1)
			if err != nil {
				return nil, err
			}
			kind, err := workbook.GetCellType(sheet, cell)
			if err != nil {
				return nil, err
			}
			if kind == excelize.CellTypeBool {
				switch rows[i][j] {
				case "1":
					rows[i][j] = "TRUE"
				case "0":
					rows[i][j] = "FALSE"
				}
			}
			rows[i][j] = escapeXLSXCell.Replace(rows[i][j])
		}
	}
	return rows, nil
}

func headerIndex(rows [][]string) int {
	for i, row := range rows {
		if blankRow(row) {
			continue
		}
		found := make(map[string]bool, len(row))
		for _, value := range row {
			found[strings.TrimSpace(value)] = true
		}
		if found["Ком"] && found["Сост"] && found["ОР"] ||
			found["Компетенция"] && found["Составляющая"] && found["Образовательный результат"] {
			return i
		}
	}
	return -1
}
