package common

import (
	"embed"
	"testing"

	"github.com/stretchr/testify/assert"
)

//go:embed testdata/embedfs
var embedFileSystemTestFS embed.FS

func TestEmbedFolderExistsOnlyReportsRegularFiles(t *testing.T) {
	assets := EmbedFolder(embedFileSystemTestFS, "testdata/embedfs")

	assert.True(t, assets.Exists("", "/static/js/app.0123456789.js"))
	for _, path := range []string{"/", "/static", "/static/", "/static/js", "/static/js/", "/static/js/missing.js"} {
		assert.False(t, assets.Exists("", path), path)
	}
}
