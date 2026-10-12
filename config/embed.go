package config

import _ "embed"

//go:embed annotations.json
var AnnotationsJSON []byte

//go:embed paths.json
var PathsJSON []byte

//go:embed rendering.json
var RenderingJSON []byte

//go:embed lists.json
var ListsJSON []byte

//go:embed bookmarks.json
var BookmarksJSON []byte
