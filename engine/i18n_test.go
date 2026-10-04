package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

var verbRe = regexp.MustCompile(`%[-+#0]*[\d.]*[vTtbcdoOqxXUeEfFgGspw%]`)

// Le traduzioni devono avere gli stessi segnaposto, nello stesso ordine, e una chiave usata nel codice
func TestEnglishStrings(t *testing.T) {
	used := map[string]bool{}
	files, _ := filepath.Glob("*.go")
	callRe := regexp.MustCompile(`T\(("(?:[^"\\]|\\.)*")\)`)
	for _, f := range files {
		data, _ := os.ReadFile(f)
		for _, m := range callRe.FindAllStringSubmatch(string(data), -1) {
			if s, err := strconv.Unquote(m[1]); err == nil {
				used[s] = true
			}
		}
	}
	for it, en := range enStrings {
		if !used[it] {
			t.Errorf("chiave non usata nel codice (refuso?): %q", it)
		}
		a, b := verbRe.FindAllString(it, -1), verbRe.FindAllString(en, -1)
		if len(a) != len(b) {
			t.Errorf("segnaposto diversi:\n it %q\n en %q", it, en)
			continue
		}
		for i := range a {
			if a[i] != b[i] {
				t.Errorf("segnaposto diversi:\n it %q\n en %q", it, en)
				break
			}
		}
	}
}
