package parse

import (
	"encoding/json"
	"strings"
	"sync"

	"kogen-go/internal/cli/data"
)

type movedRow struct {
	Match   string `json:"match"`
	Form    string `json:"form"`
	Message string `json:"message"`
}

type movedTable struct {
	Rows []movedRow `json:"rows"`
}

var (
	movedOnce  sync.Once
	movedRules []movedRow
)

func loadMovedRules() []movedRow {
	movedOnce.Do(func() {
		contents, err := data.ReadFile("moved.json")
		if err != nil {
			panic("read embedded moved-form table: " + err.Error())
		}
		var table movedTable
		if err := json.Unmarshal(contents, &table); err != nil {
			panic("decode embedded moved-form table: " + err.Error())
		}
		movedRules = table.Rows
	})
	return movedRules
}

// findMovedForm applies the frozen moved-form table before tree or option
// parsing. Prefix rows are checked as a group before flag rows, in table order.
func findMovedForm(args []string) (string, bool) {
	rules := loadMovedRules()
	for _, row := range rules {
		if row.Match != "prefix" {
			continue
		}
		words := strings.Fields(row.Form)
		if len(args) < len(words) {
			continue
		}
		match := true
		for i, word := range words {
			if args[i] != word {
				match = false
				break
			}
		}
		if match {
			return row.Message, true
		}
	}

	providerCommand := len(args) > 0 && args[0] == "provider"
	for _, row := range rules {
		if row.Match != "flag" && row.Match != "flag_outside_provider" {
			continue
		}
		if row.Match == "flag_outside_provider" && providerCommand {
			continue
		}
		for _, arg := range args {
			if arg == row.Form || strings.HasPrefix(arg, row.Form+"=") {
				return row.Message, true
			}
		}
	}
	return "", false
}
