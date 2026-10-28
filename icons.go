package main

import (
	"embed"

	"github.com/egoist/mygo/ui"
)

//go:embed icons/*.svg
var iconFiles embed.FS

func icon(name string) *ui.SVG {
	b, err := iconFiles.ReadFile("icons/" + name + ".svg")
	if err != nil {
		panic(err)
	}
	return ui.MustParseSVG(b)
}

var (
	iconPencil  = icon("pencil")
	iconDots    = icon("dots")
	iconArrow   = icon("arrow-right")
	iconX       = icon("x")
	iconAlert   = icon("alert")
	iconRefresh = icon("refresh")
)
