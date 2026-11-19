// Package design contains the reviewed tokens and workbench visuals shared with the prototype.
package design

import (
	"embed"
	"io/fs"
)

//go:embed prototype/omni-relay-avatar.png prototype/omni-relay-logo-bicolor.png
//go:embed prototype/omni-relay-colors.css prototype/omni-relay-theme.js
//go:embed prototype/omni-relay-ui.css prototype/omni-relay-ui.js prototype/omni-relay-interactions.css
//go:embed prototype/omni-relay-loading.css prototype/omni-relay-loading.js
//go:embed prototype/omni-relay-system.css prototype/omni-relay-content.css prototype/omni-relay-login-motion.js
//go:embed prototype/omni-relay-project.css
//go:embed prototype/omni-relay-bot-chat.css prototype/omni-relay-split-pane.js
var assets embed.FS

func WorkbenchFiles() fs.FS {
	files, err := fs.Sub(assets, "prototype")
	if err != nil {
		panic(err)
	}
	return files
}
