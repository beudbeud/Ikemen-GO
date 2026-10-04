package main

import (
	"testing"

	"gopkg.in/ini.v1"
)

func TestMigrateLegacyMenuCursor(t *testing.T) {
	load := func(s string) *ini.File {
		t.Helper()
		f, err := LoadINIText(s, ini.LoadOptions{InsensitiveSections: true, AllowShadows: true})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	user := load("[Title Info]\nmenu.item.spacing = 0, 16\nmenu.cursor.spr = 180,0\nmenu.cursor.offset = -17,0\n" +
		"[Option Info]\nmenu.cursor.spr = 180,0\n" +
		"[Replay Info]\nmenu.cursor.spr = 180,0\nmenu.item.active.bg.anim = 5\n")
	defs := load("[Option Info]\nmenu.item.spacing = 0, 13\n")
	migrateLegacyMenuCursor(user, defs)

	check := func(sec, key, want string) {
		t.Helper()
		if s := user.Section(sec); !s.HasKey(key) || s.Key(key).String() != want {
			t.Errorf("[%s] %s = %q, want %q", sec, key, s.Key(key).String(), want)
		}
	}
	check("Title Info", "menu.item.active.bg.spr", "180,0")
	check("Title Info", "menu.item.active.bg.offset", "-17,0")
	check("Title Info", "menu.item.active.bg.spacing", "0, 16")
	check("Option Info", "menu.item.active.bg.spacing", "0, 13") // from defaults
	if user.Section("Title Info").HasKey("menu.cursor.spr") {
		t.Error("legacy key left behind")
	}
	// Native keys present: the pack knows better, leave it untouched.
	if s := user.Section("Replay Info"); !s.HasKey("menu.cursor.spr") || s.HasKey("menu.item.active.bg.spr") {
		t.Error("[Replay Info] should not be migrated")
	}
}

func TestHiresSelect(t *testing.T) {
	load := func(s string) *ini.File {
		t.Helper()
		f, err := LoadINIText(s, ini.LoadOptions{InsensitiveSections: true, AllowShadows: true})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	// Super Marvel vs Capcom EoH: select on 640x480, no localcoord.
	hires := "[Select Info]\npos = 161,39\ntitle.offset = -9999,-9999\np2.face.offset = 639,-6\n" +
		"p2.name.offset = 615,316\nstage.pos = 158,453\np2.teammenu.pos = 622, 45\n"
	for _, c := range []struct {
		name, text string
		want       bool
	}{
		{"hires", hires, true},
		{"declared localcoord", "[Info]\nlocalcoord = 320,240\n" + hires, false},
		{"lowres", "[Select Info]\np2.face.offset = 302,13\np2.name.offset = 311,49\nstage.pos = 160,237\n", false},
		{"lowres, name and stage hidden off screen", "[Select Info]\np2.name.offset = 999,999\nstage.pos = 160,300\n", false},
		{"no select screen", "[Title Info]\nmenu.pos = 160,232\n", false},
	} {
		if got := hiresSelect(load(c.text)); got != c.want {
			t.Errorf("%s: hiresSelect = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLegacyMenu(t *testing.T) {
	load := func(s string) *ini.File {
		t.Helper()
		f, err := LoadINIText(s, ini.LoadOptions{InsensitiveSections: true, AllowShadows: true})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	// Ultimate Cosmos: 1280x720 motif, options menu in 320x240 numbers.
	cosmos := "[Info]\nlocalcoord = 1280,720\n[Option Info]\ntitle.offset = 640,38\nmenu.pos = 85, 40\n" +
		"menu.item.font = 10,0,1\nmenu.item.scale = .25, .25\nmenu.arrow.up.spr = 400, 0\n"
	for _, c := range []struct {
		name, text, sec string
		want            bool
	}{
		{"cosmos options", cosmos, "Option Info", true},
		{"other section", cosmos, "Title Info", false},
		{"ikemenversion", "[Info]\nikemenversion = 1.0\n[Option Info]\nmenu.pos = 85, 40\n", "Option Info", false},
		{"uselocalcoord 1", "[Option Info]\nmenu.uselocalcoord = 1\nmenu.pos = 85, 40\n", "Option Info", false},
		{"uselocalcoord 0", "[Replay Info]\nmenu.uselocalcoord = 0\nmenu.pos = 400, 300\n", "Replay Info", true},
		{"menu on the motif localcoord", "[Option Info]\nmenu.pos = 340, 150\n", "Option Info", false},
		{"section not in the motif", "[Info]\nlocalcoord = 1280,720\n", "Option Info", false},
	} {
		if got := legacyMenu(load(c.text), c.sec); got != c.want {
			t.Errorf("%s: legacyMenu = %v, want %v", c.name, got, c.want)
		}
	}

	user := load(cosmos).Section("Option Info")
	merged := load("[Option Info]\ntitle.font = 1\nmenu.item.font = 10,0,1\nmenu.item.bg.anim = -1\n" +
		"menu.arrow.up.spr = 400, 0\nmenu.boxcursor.col = 0,0,0\nkeymenu.item.font = 1\nfadein.anim = -1\n").Section("Option Info")
	for prefix, want := range map[string]bool{
		"menu.item.": true, "menu.boxcursor.": true, "keymenu.item.": true, // texts and boxes
		"menu.item.bg.": false, "menu.arrow.up.": false, // sprites
		"title.":  false, // the motif places it
		"fadein.": false,
	} {
		if got := legacyMenuElement(merged, user, prefix); got != want {
			t.Errorf("legacyMenuElement(%q) = %v, want %v", prefix, got, want)
		}
	}
}
