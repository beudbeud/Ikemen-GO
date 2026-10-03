package main

import (
	"reflect"
	"regexp"
	"testing"
)

func TestWarmBgmRegexp(t *testing.T) {
	re := regexp.MustCompile(`(?im)^[ \t]*bgm[ \t]*=[ \t]*([^;\r\n]*?)[ \t]*(?:;|\r?$)`)
	text := "[Scene 0]\r\nbgm = intropage1.mp3\r\n;bgm = no.mp3\r\n  bgm = Icare-Atlas echo.mp3 ; comment\nbgm.loop = 1\nbgm =\n"
	var got []string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		if m[1] != "" {
			got = append(got, m[1])
		}
	}
	if len(got) != 2 || got[0] != "intropage1.mp3" || got[1] != "Icare-Atlas echo.mp3" {
		t.Fatalf("got %q", got)
	}
	type inner struct{ Storyboard string }
	type outer struct {
		A inner
		B struct{ C inner }
		D *inner
	}
	sb := motifStoryboards(reflect.ValueOf(outer{A: inner{"a.def"}, B: struct{ C inner }{inner{"c.def"}}, D: &inner{"d.def"}}))
	if len(sb) != 2 || sb[0] != "a.def" || sb[1] != "c.def" {
		t.Fatalf("storyboards %q", sb)
	}
}
