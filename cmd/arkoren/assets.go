package main

import (
	"embed"
)

// newTemplates contains source and setup instructions. Templates are
// data rather than CLI dependencies.
//
//go:embed all:templates
var newTemplates embed.FS

// skillSource embeds the authored consumer skill and all reference pages.
// The all prefix retains dot- and underscore-prefixed files and directories,
// matching the complete source tree rather than Go's default embed filtering.
//
//go:embed all:skills/arkoren
var skillSource embed.FS
