package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestSandboxPlan_AnAllowWriteLinkToAMissingTargetInsideAClosedPathIsRefused(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)
	require.NoError(t, os.MkdirAll(filepath.Join(r.work, ".git", "hooks"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(r.work, ".git", "hooks", "pre-commit"), filepath.Join(r.work, "abs")))
	require.NoError(t, os.Symlink(filepath.Join(".husky", "pre-push"), filepath.Join(r.work, "rel")))

	for _, bad := range []string{"abs", "rel", "rel/more"} {
		_, note := r.writeNote(t, bad)

		assert.Contains(t, note, "overlaps the protected path", bad)
	}
}

func TestSandboxPlan_AGitFolderThatDoesNotExistYetIsDeniedForWrite(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)

	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Contains(t, r.f.spec.Sandbox.DenyWrite, filepath.Join(r.work, ".git"))
}

func TestSandboxPlan_HooksAndAgentSettingsAreDeniedForWriteByPatternAtAnyDepth(t *testing.T) {
	t.Parallel()

	r := newSandboxRig(t)

	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)

	got := r.f.spec.Sandbox.DenyWritePatterns

	for _, name := range []string{".git/hooks", ".husky", ".githooks", ".claude/hooks", ".claude/settings*.json"} {
		for _, base := range []string{filepath.Join(r.work, name), filepath.Join(r.work, "**", name)} {
			assert.Contains(t, got, base, name)
			assert.Contains(t, got, base+"/**", name)
		}
	}

	assert.NotContains(t, got, filepath.Join(r.work, "**", ".claude"), "the CLI keeps its state in .claude")
	assert.NotContains(t, r.f.spec.Sandbox.DenyPatterns, filepath.Join(r.work, "**", ".husky"), "hooks stay readable")
}
