package scripts_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	watchdogUnit = "idle-selfdestruct.service"
	pinnedGo     = "go1.27.4"
	done         = "HOST-SETUP-DONE"
	noWatchdog   = "no self-delete watchdog runs on this host"
	fakeMakefile = "GOLANGCI_LINT_VERSION := v2.15.0\nGOTESTSUM_VERSION := v1.13.0\nAI_RULEZ_VERSION := v5.9.2\n" +
		"GITLEAKS_VERSION := v8.30.1\nGREMLINS_VERSION := v0.6.0\n"
	unitProof = "systemd-run --user --wait --pipe"
)

// goInstalledTools are the fakes in ~/go/bin, each with the first line of its version output.
var goInstalledTools = map[string]string{
	"golangci-lint":    "golangci-lint has version 2.15.0 built with go1.27.4",
	"ai-rulez":         "ai-rulez version 5.9.2",
	"gotestsum":        "gotestsum version v1.13.0",
	"gitleaks":         "8.30.1",
	"gremlins":         "gremlins version 0.6.0 linux/amd64",
	"go-test-coverage": "unused",
	"govulncheck":      "Go: go1.27.4\nScanner: govulncheck@v1.9.0",
	"deadcode":         "unused",
	"dolt":             "dolt version 1.82.4",
	"bd":               "bd version 1.3.2",
	"actionlint":       "v1.7.12",
}

// systemTools are the fakes on the default PATH of the user manager.
var systemTools = map[string]string{
	"uv":         "uv 0.13.6 (abc 2026-10-27)",
	"shellcheck": "ShellCheck - shell script analysis tool\nversion: 0.10.0",
	"bats":       "Bats 1.10.0",
}

// provedTools are the tools that the script proves through a user unit.
var provedTools = []string{
	"go", "golangci-lint", "ai-rulez", "gotestsum", "gitleaks", "gremlins", "go-test-coverage", "govulncheck",
	"deadcode", "dolt", "bd", "jq", "uv", "shellcheck", "actionlint", "bats",
}

// fakeHost is one machine for the script: a clone, a home, a folder that stands for /, the work root and the fakes.
type fakeHost struct {
	t                                                    *testing.T
	root, source                                         *os.Root
	base, clone, home, sysroot, workRoot, bin, log, down string
	env                                                  []string
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()

	base := t.TempDir()
	h := &fakeHost{
		t: t, root: openRoot(t, base), source: openRoot(t, "."), base: base, clone: filepath.Join(base, "clone"), home: filepath.Join(base, "home"),
		sysroot: filepath.Join(base, "root"), workRoot: filepath.Join(base, "work"), bin: filepath.Join(base, "bin"), log: filepath.Join(base, "log"),
		down: filepath.Join(base, "dl"),
	}
	state := filepath.Join(base, "state")
	tmp := filepath.Join(base, "tmp")

	for _, dir := range []string{h.clone + "/scripts", h.home + "/go/bin", h.sysroot + "/usr/local/go/bin", h.workRoot, h.bin, h.down, state, tmp} {
		require.NoError(t, h.root.MkdirAll(h.rel(dir), 0o750))
	}

	for _, script := range []string{"host-setup.sh", "host-tools.sh"} {
		h.copy(script, filepath.Join(h.clone, "scripts", script), 0o644)
	}

	h.write(filepath.Join(h.clone, "go.mod"), "module example.com/h\n\ngo 1.27\n\ntoolchain "+pinnedGo+"\n", 0o644)
	h.write(filepath.Join(h.clone, "Makefile"), fakeMakefile, 0o644)
	h.write(h.log, "", 0o644)
	h.installFakes()

	jq, err := exec.LookPath("jq")
	require.NoError(t, err, "the tests of the host setup need jq")

	h.env = []string{
		"PATH=" + h.bin + ":/usr/bin:/bin", "HOME=" + h.home, "TMPDIR=" + tmp, "HOST_SETUP_SYSROOT=" + h.sysroot,
		"FAKE_LOG=" + h.log, "FAKE_BIN=" + h.bin, "FAKE_STATE=" + state, "FAKE_DL=" + h.down, "REAL_JQ=" + jq,
	}

	return h
}

func (h *fakeHost) installFakes() {
	h.t.Helper()

	fakes, err := fs.ReadDir(h.source.FS(), "testdata/fakes")
	require.NoError(h.t, err)

	for _, fake := range fakes {
		h.copy(filepath.Join("testdata/fakes", fake.Name()), filepath.Join(h.bin, fake.Name()), 0o755)
	}

	for name, line := range goInstalledTools {
		h.tool(filepath.Join(h.home, "go/bin", name), line)
	}

	for name, line := range systemTools {
		h.tool(filepath.Join(h.bin, name), line)
	}

	h.write(filepath.Join(h.sysroot, "usr/local/go/bin/go"), fakeGo(pinnedGo), 0o755)
}

// tool writes a fake tool that prints its version text.
func (h *fakeHost) tool(path, text string) {
	h.t.Helper()
	h.write(path, "#!/bin/sh\nprintf '%s\\n' '"+text+"'\n", 0o755)
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })

	return root
}

// rel turns a path under the temporary folder into a name inside its root.
func (h *fakeHost) rel(path string) string {
	h.t.Helper()

	name, err := filepath.Rel(h.base, path)
	require.NoError(h.t, err)

	return name
}

func (h *fakeHost) write(path, text string, mode os.FileMode) {
	h.t.Helper()
	require.NoError(h.t, h.root.MkdirAll(filepath.Dir(h.rel(path)), 0o750))
	require.NoError(h.t, h.root.WriteFile(h.rel(path), []byte(text), mode))
}

func (h *fakeHost) read(path string) string {
	h.t.Helper()

	data, err := h.root.ReadFile(h.rel(path))
	require.NoError(h.t, err)

	return string(data)
}

// copy copies a file of the scripts folder to a path under the temporary folder.
func (h *fakeHost) copy(from, to string, mode os.FileMode) {
	h.t.Helper()

	data, err := h.source.ReadFile(from)
	require.NoError(h.t, err)
	h.write(to, string(data), mode)
}

// run runs the script of the clone with the arguments and the extra environment, and returns its output.
func (h *fakeHost) run(extra []string, args ...string) (string, error) {
	h.t.Helper()

	cmd := exec.CommandContext(h.t.Context(), "sh")
	cmd.Args = append(cmd.Args, filepath.Join(h.clone, "scripts/host-setup.sh"))
	cmd.Args = append(cmd.Args, args...)
	cmd.Dir = h.clone
	cmd.Env = append(append([]string{}, h.env...), extra...)
	out, err := cmd.CombinedOutput()

	return string(out), err
}

func (h *fakeHost) setup(extra ...string) (string, error) {
	h.t.Helper()

	return h.run(extra, "--work-root", h.workRoot, "--watchdog", watchdogUnit)
}

func (h *fakeHost) record() string {
	h.t.Helper()

	return h.read(filepath.Join(h.workRoot, ".beehave/host-setup.record"))
}

func (h *fakeHost) envFile() string {
	return filepath.Join(h.home, ".config/environment.d/60-beehave.conf")
}

// fakeGo is a go command of a version. With `version -m` it prints the module line of the named binary.
// With `env GOPATH` it prints FAKE_GOPATH, or ~/go when that is not set.
func fakeGo(version string) string {
	return "#!/bin/sh\nif [ \"$1 $2\" = 'version -m' ]; then printf '%s\\n\\tmod example.com/tool v0.1.0 h1:x=\\n' \"$3\"; exit 0; fi\n" +
		"if [ \"$1 $2\" = 'env GOPATH' ]; then echo \"${FAKE_GOPATH:-$HOME/go}\"; exit 0; fi\n" +
		"echo 'go version " + version + " linux/amd64'\n"
}

func lastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	return lines[len(lines)-1]
}

func TestHostSetup_RefusesToFinishWithoutAnActiveWatchdog(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		env        string
		installsAt bool
	}{
		{name: "an_inactive_unit", env: "FAKE_WD_ACTIVE=inactive"},
		{name: "a_disabled_unit", env: "FAKE_WD_ENABLED=disabled"},
		{name: "a_unit_that_restarts_only_on_failure", env: "FAKE_WD_RESTART=on-failure"},
		{name: "a_watchdog_that_stops_during_the_run", env: "FAKE_STOP_WATCHDOG=1", installsAt: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.setup(tc.env)

			require.Error(t, err)
			assert.Contains(t, out, noWatchdog)
			assert.NotContains(t, out, done)
			assert.Equal(t, tc.installsAt, strings.Contains(h.read(h.log), "apt-get install"), "the installs run only after a first watchdog check passes")
		})
	}
}

func TestHostSetup_RefusesAWorkRootThatSystemdWouldExpand(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, workRoot, message string }{
		{name: "a_percent_sign", workRoot: "/srv/jobs%h", message: "must not hold a percent or dollar sign"},
		{name: "a_dollar_sign", workRoot: "/srv/$USER", message: "must not hold a percent or dollar sign"},
		{name: "a_control_character", workRoot: "/srv/a\tb", message: "must hold no control character"},
		{name: "a_relative_path", workRoot: "srv/jobs", message: "must be an absolute path"},
		{name: "no_work_root", workRoot: "", message: "--work-root is required"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.run(nil, "--work-root", tc.workRoot, "--watchdog", watchdogUnit)

			require.Error(t, err)
			assert.Contains(t, out, tc.message)
			assert.NotContains(t, h.read(h.log), "apt-get", "the refusal comes before any change")
		})
	}
}

func TestHostSetup_RefusesBeforeAnyChange(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, env, message, remove string
		args                       []string
	}{
		{name: "the_root_user", env: "FAKE_UID=0", message: "not as root"},
		{name: "an_arm_machine", env: "FAKE_ARCH=aarch64", message: "linux amd64 only"},
		{name: "sudo_that_asks_for_a_password", env: "FAKE_SUDO_RC=1", message: "sudo without a password"},
		{name: "a_clone_without_go_mod", remove: "go.mod", message: "holds no go.mod and Makefile"},
		{name: "a_clone_without_a_makefile", remove: "Makefile", message: "holds no go.mod and Makefile"},
		{name: "no_watchdog_named", message: "--watchdog is required", args: []string{"--work-root", "/srv/jobs"}},
		{name: "a_watchdog_that_is_no_unit_name", message: "is not a systemd unit name", args: []string{"--work-root", "/srv/jobs", "--watchdog", "a b"}},
		{name: "a_watchdog_that_starts_with_a_dash", message: "is not a systemd unit name", args: []string{"--work-root", "/srv/jobs", "--watchdog", "--user"}},
		{name: "an_unknown_argument", message: "unknown argument --cloud", args: []string{"--cloud", "gcp"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			if tc.remove != "" {
				require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.clone, tc.remove))))
			}

			args := tc.args
			if args == nil {
				args = []string{"--work-root", h.workRoot, "--watchdog", watchdogUnit}
			}

			out, err := h.run([]string{tc.env}, args...)

			require.Error(t, err)
			assert.Contains(t, out, tc.message)
			assert.NotContains(t, h.read(h.log), "apt-get", "the refusal comes before any change")
		})
	}
}

func TestHostSetup_RefusesAWorkRootThatIsNotTheLoginUsers(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, workRoot, env, message string }{
		{name: "the_root_folder", workRoot: "/", message: "is a system folder"},
		{name: "a_system_folder", workRoot: "/etc", message: "is a system folder"},
		{name: "a_link_to_the_root_folder", workRoot: "link", message: "is a system folder"},
		{name: "a_folder_of_another_user", env: "FAKE_OWNER_UID=0", message: "is not owned by the login user"},
		{name: "a_file", workRoot: "file", message: "is not a folder"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			workRoot := tc.workRoot

			switch workRoot {
			case "":
				workRoot = h.workRoot
			case "link":
				workRoot = filepath.Join(h.base, "link")
				require.NoError(t, h.root.Symlink("/", "link"))
			case "file":
				workRoot = filepath.Join(h.base, "file")
				h.write(workRoot, "", 0o644)
			}

			out, err := h.run([]string{tc.env}, "--work-root", workRoot, "--watchdog", watchdogUnit)

			require.Error(t, err)
			assert.Contains(t, out, tc.message)

			log := h.read(h.log)
			assert.NotContains(t, log, "apt-get", "the refusal comes before any change")
			assert.NotContains(t, log, "sudo install -d", "an existing folder is never chowned or chmodded")
		})
	}
}

func TestHostSetup_MakesOnlyAMissingWorkRoot(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	workRoot := filepath.Join(h.base, "new/jobs")

	out, err := h.run(nil, "--work-root", workRoot, "--watchdog", watchdogUnit)
	require.NoError(t, err, out)
	assert.Contains(t, h.read(h.log), "\nsudo install -d -o builder -g builder -m 0755 "+workRoot+"\n", "a missing work root is made for the login user")

	h.write(h.log, "", 0o644)
	out, err = h.run(nil, "--work-root", workRoot, "--watchdog", watchdogUnit)
	require.NoError(t, err, out)
	assert.NotContains(t, h.read(h.log), "sudo install -d", "an existing work root is left as it is")
}

func TestHostSetup_RefusesAUvArchiveWhoseChecksumDiffers(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.bin, "uv"))))
	h.write(filepath.Join(h.down, "uv-x86_64-unknown-linux-gnu.tar.gz"), "not the pinned uv", 0o644)

	out, err := h.setup()

	require.Error(t, err)
	assert.Contains(t, out, "the checksum of uv-x86_64-unknown-linux-gnu.tar.gz does not match the pin")
	assert.NotContains(t, h.read(h.log), "sudo install -m 0755", "no uv is installed")
	assert.NotContains(t, out, done)
}

func TestHostSetup_ReadsTheGoVersionFromGoMod(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, toolchain, sha, message string
		broken, installs              bool
	}{
		{name: "installs_the_toolchain_version", toolchain: "toolchain go1.27.9\n", installs: true},
		{name: "refuses_a_go_mod_without_a_toolchain_line", message: "has no toolchain line"},
		{name: "refuses_an_archive_whose_checksum_differs", toolchain: "toolchain go1.27.9\n", sha: strings.Repeat("0", 64), message: "does not match the pin"},
		{name: "keeps_the_old_go_when_the_archive_does_not_unpack", toolchain: "toolchain go1.27.9\n", broken: true, message: "is unchanged"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			goRoot := h.sysroot + "/usr/local/go"
			h.write(filepath.Join(h.clone, "go.mod"), "module example.com/h\n\ngo 1.27\n\n"+tc.toolchain, 0o644)
			h.serveGo("go1.27.9", tc.sha, tc.broken)

			out, err := h.setup()
			log := h.read(h.log)

			if !tc.installs {
				require.Error(t, err)
				assert.Contains(t, out, tc.message)
				assert.NotContains(t, log, "sudo rm -rf "+goRoot+"\n")
				assert.NotContains(t, log, "sudo mv ")
				assert.Contains(t, h.read(goRoot+"/bin/go"), pinnedGo, "the old Go stays")

				return
			}

			require.NoError(t, err, out)
			assert.Contains(t, log, "https://go.dev/dl/go1.27.9.linux-amd64.tar.gz")
			assert.Contains(t, log, "sudo tar -C "+goRoot+".new -xzf ")
			assert.Less(t, strings.Index(log, "sudo tar -C "+goRoot+".new"), strings.Index(log, "sudo mv "+goRoot+" "+goRoot+".new/old\n"),
				"the old Go moves out only after the new one unpacks")
			assert.Contains(t, log, "sudo mv "+goRoot+".new/go "+goRoot+"\n")
			assert.Contains(t, h.record(), "tool go go version go1.27.9 linux/amd64\n")
		})
	}
}

// serveGo puts a Go archive of the version and the go.dev list that names it in the downloads. An empty sha means the true one.
// A broken archive has the true sha but does not unpack. serveGo also puts the fakes of the swap on the PATH.
func (h *fakeHost) serveGo(version, sha string, broken bool) {
	h.t.Helper()

	var archive bytes.Buffer

	zw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(zw)
	body := fakeGo(version)
	require.NoError(h.t, tw.WriteHeader(&tar.Header{Name: "go/bin/go", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte(body))
	require.NoError(h.t, err)
	require.NoError(h.t, tw.Close())
	require.NoError(h.t, zw.Close())

	if broken {
		archive.Truncate(archive.Len() / 2)
	}

	for _, real := range []string{"/usr/bin/tar", "/bin/mv", "/bin/mkdir"} {
		h.write(filepath.Join(h.bin, filepath.Base(real)), "#!/bin/sh\nexec "+real+" \"$@\"\n", 0o755)
	}

	if sha == "" {
		sum := sha256.Sum256(archive.Bytes())
		sha = hex.EncodeToString(sum[:])
	}

	file := version + ".linux-amd64.tar.gz"
	h.write(filepath.Join(h.down, file), archive.String(), 0o644)
	h.write(filepath.Join(h.down, "go.json"), `[{"version":"`+version+`","files":[{"filename":"`+file+`","sha256":"`+sha+`"}]}]`, 0o644)
}

func TestHostSetup_ProvesEachToolThroughAUserUnit(t *testing.T) {
	t.Parallel()

	t.Run("every_tool_runs", func(t *testing.T) {
		t.Parallel()

		h := newFakeHost(t)
		out, err := h.setup()

		require.NoError(t, err, out)
		assert.Equal(t, done, lastLine(out))
		assert.Equal(t, len(provedTools), strings.Count(h.read(h.log), unitProof+" --quiet --collect -p WorkingDirectory="+h.clone+" sh -c "))

		record := h.record()
		for _, name := range provedTools {
			assert.Contains(t, record, "\ntool "+name+" ", name)
		}

		assert.Contains(t, record, "tool deadcode \tmod example.com/tool v0.1.0 h1:x=\n")
		assert.Contains(t, record, "tool govulncheck \tmod example.com/tool v0.1.0 h1:x=\n", "the govulncheck line names govulncheck, not Go")
		assert.Contains(t, record, "tool shellcheck version: 0.10.0\n")
		assert.Contains(t, out, record, "the script prints the record")
	})

	t.Run("a_tool_that_reads_its_input_does_not_end_the_list", func(t *testing.T) {
		t.Parallel()

		h := newFakeHost(t)
		h.write(filepath.Join(h.home, "go/bin/dolt"), "#!/bin/sh\ncat >/dev/null\necho 'dolt version 1.82.4'\n", 0o755)

		out, err := h.setup()

		require.NoError(t, err, out)

		record := h.record()
		for _, name := range provedTools {
			assert.Contains(t, record, "\ntool "+name+" ", name)
		}
	})

	t.Run("a_missing_tool_fails", func(t *testing.T) {
		t.Parallel()

		h := newFakeHost(t)
		require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.home, "go/bin/gremlins"))))

		out, err := h.setup()

		require.Error(t, err)
		assert.Contains(t, out, "gremlins does not run in a user unit")
		assert.NotContains(t, out, done)
	})
}

func TestHostSetup_FailsWhenAToolIsNotAtItsPin(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, tool, text, message string }{
		{name: "an_old_gotestsum", tool: "gotestsum", text: "gotestsum version v1.12.0", message: "gotestsum is not at its pin 1.13.0"},
		{name: "a_longer_version", tool: "gremlins", text: "gremlins version 0.6.01", message: "gremlins is not at its pin 0.6.0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			h.tool(filepath.Join(h.home, "go/bin", tc.tool), tc.text)

			out, err := h.setup()

			require.Error(t, err)
			assert.Contains(t, out, tc.message)
			assert.Contains(t, out, tc.text, "the failure names the version found")
			assert.NotContains(t, out, done)
		})
	}
}

func TestHostSetup_PutsTheToolFoldersOnThePathOfTheUserManager(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	h.write(h.envFile(), "PATH=/stale\n", 0o644)

	out, err := h.setup()

	require.NoError(t, err, out)
	assert.Equal(t, "PATH="+h.sysroot+"/usr/local/go/bin:"+h.home+"/go/bin:${PATH}\nBEEHAVE_BD_132="+h.home+"/go/bin/bd\n", h.read(h.envFile()))

	log := h.read(h.log)
	assert.Contains(t, log, "systemctl --user daemon-reload\n")
	assert.Less(t, strings.Index(log, "systemctl --user daemon-reload"), strings.Index(log, unitProof), "the reload comes before the proof")
	assert.Contains(t, log, "make setup PATH="+h.sysroot+"/usr/local/go/bin:"+h.home+"/go/bin:")
	assert.Contains(t, log, "make contract-tools-install PATH=")
}

func TestHostSetup_ReadsTheGoPathFromGo(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	gopath := filepath.Join(h.base, "gopath")
	for name, line := range goInstalledTools {
		h.tool(filepath.Join(gopath, "bin", name), line)
		require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.home, "go/bin", name))))
	}

	out, err := h.setup("FAKE_GOPATH=" + gopath + ":" + filepath.Join(h.base, "second"))

	require.NoError(t, err, out)
	assert.Equal(t, "PATH="+h.sysroot+"/usr/local/go/bin:"+gopath+"/bin:${PATH}\nBEEHAVE_BD_132="+gopath+"/bin/bd\n", h.read(h.envFile()))
	assert.Contains(t, h.read(h.log), "make setup PATH="+h.sysroot+"/usr/local/go/bin:"+gopath+"/bin:")
	assert.Contains(t, out, "host-tools: actionlint v1.7.12 is installed", "host-tools finds actionlint in the GOPATH")
	assert.Equal(t, done, lastLine(out))
}

func TestHostSetup_ASecondRunChangesNothing(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup()
	require.NoError(t, err, out)

	past := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, h.root.Chtimes(h.rel(h.envFile()), past, past))
	first := withoutTime(h.record())
	firstLog := h.read(h.log)
	assert.Contains(t, firstLog, "\nsudo loginctl enable-linger builder\n", "the first run turns on linger")
	assert.Contains(t, firstLog, "\nsystemctl --user daemon-reload\n", "the first run writes the environment file")
	h.write(h.log, "", 0o644)

	out, err = h.setup()
	require.NoError(t, err, out)

	info, err := h.root.Stat(h.rel(h.envFile()))
	require.NoError(t, err)
	assert.True(t, info.ModTime().Equal(past), "the environment file is not written again")
	assert.Equal(t, first, withoutTime(h.record()))

	log := h.read(h.log)
	for _, change := range []string{"\nsystemctl --user daemon-reload", "\nsudo loginctl enable-linger", "\ncurl ", "\nsudo tar ", "\nsudo rm "} {
		assert.NotContains(t, log, change)
	}
}

func withoutTime(record string) string {
	var kept []string

	for line := range strings.SplitSeq(record, "\n") {
		if !strings.HasPrefix(line, "at=") {
			kept = append(kept, line)
		}
	}

	return strings.Join(kept, "\n")
}

func TestHostSetup_AKernelWithoutLandlockIsAWarning(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, lsm, kernel, line string }{
		{name: "no_landlock_module", lsm: "lockdown,capability,yama,apparmor", kernel: "6.8.0-1015-gcp", line: "landlock warning lockdown,capability,yama,apparmor 6.8.0-1015-gcp"},
		{name: "a_kernel_older_than_6_7", lsm: "lockdown,landlock,apparmor", kernel: "6.5.0-1", line: "landlock warning lockdown,landlock,apparmor 6.5.0-1"},
		{name: "no_lsm_file", kernel: "6.8.0-1015-gcp", line: "landlock warning none 6.8.0-1015-gcp"},
		{name: "a_kernel_with_landlock", lsm: "lockdown,capability,landlock,yama,apparmor", kernel: "6.8.0-1015-gcp", line: "landlock pass"},
		{name: "a_major_version_above_6", lsm: "landlock", kernel: "7.0.1", line: "landlock pass"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			if tc.lsm != "" {
				h.write(filepath.Join(h.sysroot, "sys/kernel/security/lsm"), tc.lsm+"\n", 0o644)
			}

			out, err := h.setup("FAKE_KERNEL=" + tc.kernel)

			require.NoError(t, err, out)
			assert.Equal(t, done, lastLine(out))
			assert.Contains(t, h.record(), "\n"+tc.line+"\n")
		})
	}
}
