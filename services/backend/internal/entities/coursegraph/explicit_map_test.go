package coursegraph

import (
	"encoding/json"
	"testing"
)

func TestMapIgnoresAuthoredDependenciesAndRetainsTheFullBank(t *testing.T) {
	header := append(append([]string{}, mapHeaderRow...), "ID темы", "Пререквизиты", "Текст темы")
	rows := [][]string{header}
	for i, bloom := range []string{"Помнить", "Понимать", "Анализировать", "Применять"} {
		row := mapRow("Авторская карта", string(rune('А'+i)), bloom, "3", "TRUE", "Вопрос?")
		rows = append(rows, append(row, "duplicate", "missing; duplicate", "Теория"))
	}
	built, shape := buildShape(t, rows)
	if built.Topics != 3 || len(shape.Prerequisites) != 2 {
		t.Fatalf("selection: topics=%d edges=%d", built.Topics, len(shape.Prerequisites))
	}
	var bank struct {
		Bank []mapTopic `json:"bank"`
	}
	if err := json.Unmarshal(built.Graph, &bank); err != nil {
		t.Fatal(err)
	}
	if len(bank.Bank) != 4 {
		t.Fatalf("bank=%d", len(bank.Bank))
	}
	for _, topic := range bank.Bank {
		if topic.Text == "" || topic.ID == "duplicate" {
			t.Fatalf("lost theory or used authored ID: %s", topic.ID)
		}
	}
}
