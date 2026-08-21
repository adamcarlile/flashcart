// Package assets holds the embedded single-page UI.
package assets

import "embed"

// FS carries index.html, style.css and app.js.
//
//go:embed index.html style.css app.js
var FS embed.FS
