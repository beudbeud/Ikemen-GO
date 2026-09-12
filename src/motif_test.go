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
