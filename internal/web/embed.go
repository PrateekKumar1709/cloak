// Package web embeds the Cloak dashboard static assets.
package web

import "embed"

// Static holds dashboard files under static/.
//
//go:embed static/*
var Static embed.FS
