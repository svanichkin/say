package lang

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

var verb = regexp.MustCompile(`%[a-z]`)

// Every language says every text, with the same values put in, in the same
// order, and with as many lines as English (the status screens' heading is
// the first line).
func TestTablesComplete(t *testing.T) {
	for code, table := range tables {
		for k := Key(0); k < numKeys; k++ {
			s, want := table[k], en[k]
			if s == "" {
				t.Errorf("%s: text %d missing", code, k)
				continue
			}
			if got, exp := verb.FindAllString(s, -1), verb.FindAllString(want, -1); !slices.Equal(got, exp) {
				t.Errorf("%s: text %d has %v, English has %v: %q", code, k, got, exp, s)
			}
			if strings.Count(s, "\n") != strings.Count(want, "\n") {
				t.Errorf("%s: text %d has %d lines, English %d: %q", code, k, strings.Count(s, "\n")+1, strings.Count(want, "\n")+1, s)
			}
		}
		for k := range table {
			if k < 0 || k >= numKeys {
				t.Errorf("%s: unknown text %d", code, k)
			}
		}
	}
}

func TestMatch(t *testing.T) {
	for tag, want := range map[string]string{
		"ru":          "ru",
		"ru-RU":       "ru",
		"ru_RU.UTF-8": "ru",
		"pt-BR":       "pt",
		"zh-Hans-CN":  "zh",
		"zh-TW":       "zh-Hant",
		"zh-Hant-HK":  "zh-Hant",
		"no":          "nb",
		"nn-NO":       "nb",
		"EN-us":       "en",
	} {
		if got, ok := Match(tag); !ok || got != want {
			t.Errorf("Match(%q) = %q, %v; want %q", tag, got, ok, want)
		}
	}
	for _, tag := range []string{"", "C", "xx-YY", "ar"} {
		if got, ok := Match(tag); ok {
			t.Errorf("Match(%q) = %q, want none", tag, got)
		}
	}
}

func TestFlagWins(t *testing.T) {
	defer current.Store(nil)
	Init([]string{"host", "-lang", "de"})
	if Code() != "de" {
		t.Fatalf("-lang de: %s", Code())
	}
	Init([]string{"--lang=ja"})
	if Code() != "ja" || T(Retry, 3) != "3 秒後に再試行" {
		t.Fatalf("--lang=ja: %s %q", Code(), T(Retry, 3))
	}
	current.Store(nil)
	if T(Attempt, 2) != "Attempt #2" {
		t.Fatalf("English by default: %q", T(Attempt, 2))
	}
}
