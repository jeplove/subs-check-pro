package script

import (
	_ "embed"
)

//go:embed sub-info.js
var EmbeddedSubInfoJS []byte

//go:embed node-split.js
var EmbeddedNodeSplitScript []byte
