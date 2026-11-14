package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

// declaredKeys returns the keys of a block in key order.
func declaredKeys(block *config.Block) []string {
	keys := make([]string, len(block.Args))
	for i, arg := range block.Args {
		keys[i] = arg.Key
	}

	slices.Sort(keys)

	return keys
}

// givenKeys returns the keys that a project file wrote in its host block, in key order.
func givenKeys(block *model.Block) []string {
	var keys []string

	for _, part := range strings.Fields(strings.Trim(block.String(), "{}")) {
		key, _, _ := strings.Cut(part, "=")
		keys = append(keys, key)
	}

	slices.Sort(keys)

	return keys
}

// This binary may not import the host packages, so it repeats their blocks. The files of testdata/hosts hold every key of each block and
// cmd/beehave reads the same files against the blocks of the real adapters. A key that one side adds or drops fails one of the two tests.
func TestRegistry_DeclaresTheKeysThatTheHostFilesHoldForEachHostBlock(t *testing.T) {
	t.Parallel()

	cfg, problems := config.LoadFolder(filepath.Join("testdata", "hosts"), nil, registry())

	require.Empty(t, problems)
	require.Len(t, cfg.Projects, 3)

	for name, project := range cfg.Projects {
		entry := registry().Hosts[project.Host.Provider]

		require.NotNil(t, entry.Block, name)
		assert.Equal(t, declaredKeys(entry.Block), givenKeys(project.Host.Block), name)
	}
}
