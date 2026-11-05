package games

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverNativeSteamLibrary(t *testing.T) {
	root := home(t)
	library := filepath.Join(root, "Library", "Application Support", "Steam")
	common := mkdir(t, filepath.Join(library, "steamapps", "common", "Game"))
	groups := Discover(root)
	require.Len(t, groups, 1)
	assert.Equal(t, "steam", groups[0].Key)
	assert.Equal(t, []Place{{Path: filepath.Dir(common), Kind: KindLibrary, Detail: "Steam"}}, groups[0].Places)
}

func TestDiscoverNativeHeroicInstalledGames(t *testing.T) {
	root := home(t)
	heroic := filepath.Join(root, "Library", "Application Support", "heroic")
	gog := mkdir(t, filepath.Join(root, "External", "GOG Game"))
	epic := mkdir(t, filepath.Join(root, "External", "Epic Game"))
	amazon := mkdir(t, filepath.Join(root, "External", "Amazon Game"))
	write(t, filepath.Join(heroic, "gog_store", "installed.json"), `{"installed":[{"install_path":`+strconv.Quote(gog)+`},{"install_path":"/missing/game"}]}`)
	write(t, filepath.Join(heroic, "legendaryConfig", "legendary", "installed.json"), `{"game":{"title":"Epic Game","install_path":`+strconv.Quote(epic)+`}}`)
	write(t, filepath.Join(heroic, "nile_config", "installed.json"), `[{"path":`+strconv.Quote(amazon)+`}]`)
	groups := Discover(root)
	require.Len(t, groups, 1)
	assert.Equal(t, "heroic", groups[0].Key)
	assert.Equal(t, []Place{
		{Path: heroic, Kind: KindConfig},
		{Path: amazon, Kind: KindLibrary, Detail: "Amazon Game"},
		{Path: epic, Kind: KindLibrary, Detail: "Epic Game"},
		{Path: gog, Kind: KindLibrary, Detail: "GOG Game"},
	}, groups[0].Places)
}
