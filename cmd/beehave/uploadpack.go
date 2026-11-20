package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	"github.com/tobocop2/beehave/internal/sandbox"
	"github.com/tobocop2/beehave/internal/sandbox/enforcers"
)

// uploadPackArg is the first argument that makes beehave run git upload-pack under a sandbox enforcer, for the copy of a head into the push repository.
const uploadPackArg = "--beehave-upload-pack"

// uploadPackWords is the number of words of a helper call: beehave, uploadPackArg, the enforcer, the folder that upload-pack may read, and the path
// that git appends.
const uploadPackWords = 5

// uploadPackFailed is the exit code of the helper when upload-pack could not start under the enforcer. Upload-pack then ran no code.
const uploadPackFailed = 125

// chooseEnforcer returns the enforcer that the sandbox_enforcer value name asks for, or an error that wraps sandbox.ErrUnavailable and says why none runs.
func chooseEnforcer(name string) (sandbox.Enforcer, error) { //nolint:ireturn // the table holds each enforcer by the interface
	table := enforcers.Table()

	key, err := sandbox.Choose(table, enforcers.Order(), name)
	if err != nil {
		return nil, err
	}

	return table[key], nil
}

// uploadPackProgram returns the --upload-pack value of the copy: beehave in the helper mode, with the enforcer and the folder that upload-pack may read,
// each word quoted for the shell that git starts it with.
func uploadPackProgram(self, enforcer, folder string) string {
	words := []string{self, uploadPackArg, enforcer, folder}
	for i, word := range words {
		words[i] = model.ShellQuote(word)
	}

	return strings.Join(words, " ")
}

// uploadPackPlan is the plan of upload-pack: read on the folder and the system folders, write on the null device only, which git opens at start, and no
// network.
func uploadPackPlan(folder string) sandbox.Plan {
	return sandbox.Plan{Read: append(sandbox.SystemRead(), folder), Write: []string{os.DevNull}}
}

// helperIO is the standard input and output of the helper, which upload-pack gets as they are.
type helperIO struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

// runUploadPack is called first in main. When args are a helper call, it runs git upload-pack on the path that git appended, under the enforcer that
// choose returns for the name in args and the plan of the folder in args, and returns the exit code and true. The search path of git is the PATH of env.
// When args are not a helper call, it returns 0 and false at once.
func runUploadPack(ctx context.Context, args []string, env map[string]string, choose func(string) (sandbox.Enforcer, error), std helperIO) (int, bool) {
	if len(args) != uploadPackWords || args[1] != uploadPackArg {
		return 0, false
	}

	code, err := uploadPack(ctx, args[2], args[3], args[4], env, choose, std)
	if err != nil {
		fmt.Fprintf(std.stderr, "beehave upload-pack: %v. The copy of the head did not start.\n", err)

		return uploadPackFailed, true
	}

	return code, true
}

// uploadPack runs git upload-pack on path under the enforcer that choose returns for name, with the plan of folder, and returns its exit code.
// Any wanted object is served: the plan, not a ref, limits what upload-pack can read.
func uploadPack(ctx context.Context, name, folder, path string, env map[string]string, choose func(string) (sandbox.Enforcer, error), std helperIO) (int, error) {
	enforcer, err := choose(name)
	if err != nil {
		return 0, err
	}

	return sandbox.Execute(ctx, enforcer, uploadPackPlan(folder), sandbox.Command{
		Argv: []string{gitexec.Binary, "-c", "uploadpack.allowAnySHA1InWant=true", "upload-pack", path}, Dir: folder, Env: []string{"PATH=" + env["PATH"]},
		Stdin: std.stdin, Stdout: std.stdout, Stderr: std.stderr,
	})
}
