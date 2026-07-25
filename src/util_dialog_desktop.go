//go:build !raw && !android && !libretro

package main

import (
	"github.com/sqweek/dialog"
)

// Message box implementation
//
// Split out of util_desktop.go so the libretro core keeps everything else in
// that file (TTF fonts, renderer selection) without pulling in dialog, whose
// cgo preamble needs GTK 3 -- a dependency an embedded frontend image should
// not have to carry for two message boxes it can never show.
func ShowInfoDialog(message, title string) {
	dialog.Message(message).Title(title).Info()
}

func ShowErrorDialog(message string) {
	dialog.Message(message).Title("I.K.E.M.E.N Error").Error()
}
