package scripts_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rootDone    = "HOST-SETUP-ROOT-DONE"
	jobUser     = "beehave-jobs"
	jobUserLog  = "useradd --create-home --home-dir /home/beehave-jobs --shell /bin/sh beehave-jobs\n"
	unsafeClone = "can be written by a user other than root and the admin"
)

// rootGroups are the groups that give root, by the group name that a Debian or Ubuntu host uses.
var rootGroups = []string{"root", "sudo", "admin", "wheel", "google-sudoers", "docker", "lxd", "disk", "shadow"}

// jobKeyFile is where the root stage puts the key of the controller for the job user.
func (h *fakeHost) jobKeyFile() string {
	return filepath.Join(h.sysroot, "home", jobUser, ".ssh/authorized_keys")
}

func TestHostSetupRoot_MakesTheJobUserWithNoSudoAndNoExtraGroup(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.rootSetup()
	require.NoError(t, err, out)

	log := h.read(h.log)
	assert.Contains(t, log, "\nsudo "+jobUserLog, "useradd makes the user: the guest agent puts only the users it makes into google-sudoers")
	assert.NotRegexp(t, `\nsudo useradd [^\n]*( -G| --groups| -g | --gid| -o | -u )`, log, "the job user gets no extra group, no shared gid and no chosen uid")
	assert.NotContains(t, log, "sudoers", "the job user gets no sudoers entry")
	assert.Equal(t, rootDone, lastLine(out))
}

func TestHostSetupRoot_PutsTheControllerKeyInTheAuthorizedKeysOfTheJobUser(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.rootSetup()
	require.NoError(t, err, out)

	assert.Equal(t, controllerKey, h.read(h.jobKeyFile()))

	log := h.read(h.log)
	assert.Contains(t, log, "\ninstall -d -m 0700 -o beehave-jobs -g beehave-jobs "+filepath.Dir(h.jobKeyFile())+"\n")
	assert.Contains(t, log, "\ninstall -m 0600 -o beehave-jobs -g beehave-jobs "+h.jobKey+" "+h.jobKeyFile()+"\n")
	assert.NotContains(t, log, "ssh-keys", "the key goes to the authorized_keys file only, never to the instance metadata, where the guest agent would read it and add the user to google-sudoers")
}

func TestHostSetupRoot_InstallsTheToolsTheGuardAndLingerBeforeItReportsDone(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.rootSetup()
	require.NoError(t, err, out)

	log := h.read(h.log)
	assert.Regexp(t, `\napt-get install [^\n]* nftables\n`, log, "the nft command that the unit runs comes from the nftables package")
	assert.Contains(t, log, "\nsudo loginctl enable-linger "+jobUser+"\n", "the job user keeps a user manager of its own")
	assert.Contains(t, log, "\nsudo systemctl restart metadata-guard.service\n")
	assert.Contains(t, out, "metadata root=200\n", "root keeps the token: the idle watchdog runs as root")
	assert.NotContains(t, log, "\nmake ", "the root stage runs no code of the clone")
	assert.NotContains(t, log, "systemd-run", "the root stage starts no job")
}

func TestHostSetupRoot_RefusesBeforeAnyChange(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		env     string
		message string
		prepare func(t *testing.T, h *fakeHost)
		args    []string
	}{
		{name: "a_user_that_is_not_root", env: "FAKE_UID=1000", message: "run the root stage as root"},
		{name: "an_arm_machine", env: "FAKE_ARCH=aarch64", message: "linux amd64 only"},
		{name: "a_clone_without_a_makefile", message: "holds no go.mod and Makefile", prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.clone, "Makefile"))))
		}},
		{name: "a_file_of_the_clone_that_the_group_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			require.NoError(t, h.root.Chmod(h.rel(filepath.Join(h.clone, "Makefile")), 0o664))
		}},
		{name: "a_file_of_the_clone_that_every_user_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			require.NoError(t, h.root.Chmod(h.rel(filepath.Join(h.clone, "scripts/host-tools.sh")), 0o646))
		}},
		{name: "a_folder_of_the_clone_that_the_group_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			require.NoError(t, h.root.Chmod(h.rel(filepath.Join(h.clone, "scripts")), 0o770))
		}},
		{name: "a_file_of_another_owner", env: "SUDO_UID=" + strconv.Itoa(os.Getuid()+1), message: unsafeClone, prepare: func(t *testing.T, _ *fakeHost) {
			t.Helper()
			if os.Getuid() == 0 {
				t.Skip("the files of the test are root files when the test runs as root")
			}
		}},
		{name: "no_job_key", message: "--job-key is required", args: []string{"--stage", "root", "--watchdog", watchdogUnit}},
		{name: "a_job_key_file_that_is_missing", message: "the job key file <base>/none does not exist", args: []string{"--stage", "root", "--watchdog", watchdogUnit, "--job-key", "<base>/none"}},
		{name: "a_private_key", message: "holds a private key", prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			h.write(h.jobKey, "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n")
		}},
		{name: "a_parent_folder_that_the_group_can_write", message: unsafeClone, prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			require.NoError(t, h.root.Chmod(".", 0o775))
		}},
		{name: "two_keys", message: "more than one line", prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			h.write(h.jobKey, controllerKey+controllerKey)
		}},
		{name: "a_line_that_is_not_a_key", message: "starts with neither", prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			h.write(h.jobKey, "command=\"x\" "+controllerKey)
		}},
		{name: "an_empty_key_file", message: "holds no public key", prepare: func(t *testing.T, h *fakeHost) {
			t.Helper()
			h.write(h.jobKey, "")
		}},
		{name: "a_work_root_argument", message: "--work-root is an argument of the job stage", args: []string{"--stage", "root", "--work-root", "/srv/w"}},
		{name: "no_watchdog", message: "--watchdog is required", args: []string{"--stage", "root", "--job-key", "<key>"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			if tc.prepare != nil {
				tc.prepare(t, h)
			}

			var out string
			var err error

			if tc.args != nil {
				args := make([]string, 0, len(tc.args))
				for _, arg := range tc.args {
					args = append(args, strings.ReplaceAll(strings.ReplaceAll(arg, "<base>", h.base), "<key>", h.jobKey))
				}

				out, err = h.sh([]string{"FAKE_UID=0", "SUDO_UID=" + strconv.Itoa(os.Getuid())}, filepath.Join(h.clone, "scripts/host-setup.sh"), args...)
			} else {
				out, err = h.rootSetup(tc.env)
			}

			require.Error(t, err)
			assert.Contains(t, out, strings.ReplaceAll(tc.message, "<base>", h.base))
			assert.NotContains(t, out, rootDone)
			assert.NotContains(t, h.read(h.log), "apt-get", "the refusal comes before any change")
			assert.NotContains(t, h.read(h.log), "useradd", "the refusal comes before any change")
		})
	}
}

func TestHostSetupRoot_RefusesAJobUserThatHasARootGroup(t *testing.T) {
	t.Parallel()

	for _, group := range rootGroups {
		t.Run(group, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.rootSetup("FAKE_JOB_GROUPS=beehave-jobs " + group)

			require.Error(t, err)
			assert.Contains(t, out, "the job user "+jobUser+" is in the group "+group+", which gives root. Remove it: sudo gpasswd -d "+jobUser+" "+group)
			assert.NotContains(t, out, rootDone)
		})
	}
}

func TestHostSetupRoot_ASecondRunChangesNothing(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.rootSetup()
	require.NoError(t, err, out)

	h.write(h.log, "")
	out, err = h.rootSetup()
	require.NoError(t, err, out)

	log := h.read(h.log)
	for _, change := range []string{"useradd", "install -d", "install -m", "enable-linger", "restart metadata-guard"} {
		assert.NotContains(t, log, change, "a second run does not repeat: "+change)
	}

	assert.Equal(t, rootDone, lastLine(out))
}

func TestHostSetupRoot_ReplacesAKeyThatDiffers(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.rootSetup()
	require.NoError(t, err, out)

	h.write(h.jobKeyFile(), "ssh-ed25519 AAAAstale old@host\n")
	out, err = h.rootSetup()
	require.NoError(t, err, out)

	assert.Equal(t, controllerKey, h.read(h.jobKeyFile()), "the key of the controller is the only key of the job user")
}

func TestHostSetupRoot_FailsWhenRootDoesNotGetTheTokenFromTheMetadataServer(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"000", "403", "404"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.rootSetup("FAKE_METADATA_ROOT_CODE=" + code)

			require.Error(t, err)
			assert.Contains(t, out, "root got HTTP "+code+" from "+metadataURL4+", not 200")
			assert.NotContains(t, out, rootDone)
		})
	}
}

func TestHostSetupRoot_FailsWhenTheMetadataGuardIsNotInPlace(t *testing.T) {
	t.Parallel()

	cases := []struct{ env, message string }{
		{env: "FAKE_GUARD_NO_START=1", message: "metadata-guard.service is not active"},
		{env: "FAKE_GUARD_NO_ENABLE=1", message: "is not enabled, so a reboot would drop the rules"},
		{env: "FAKE_NFT_EMPTY=1", message: "the metadata guard rules are not loaded"},
	}

	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.rootSetup(tc.env)

			require.Error(t, err)
			assert.Contains(t, out, tc.message)
			assert.NotContains(t, out, rootDone)
		})
	}
}

func TestHostSetupJob_UsesNoSudo(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup()
	require.NoError(t, err, out)

	assert.Empty(t, rootActions(h.read(h.log)), "a step that runs candidate code holds no root")
	assert.NotContains(t, h.read(h.log), "apt-get", "the root stage installs the packages")
	assert.NotContains(t, h.read(h.log), "sudo curl", "root is not the job stage's probe")
}

func TestHostSetupJob_FailsWithoutTheGoOfTheRootStage(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.sysroot, "usr/local/go/bin/go"))))

	out, err := h.setup()

	require.Error(t, err)
	assert.Contains(t, out, "there is no Go in "+filepath.Join(h.sysroot, "usr/local/go")+". Run the root stage")
	assert.NotContains(t, out, done)
}

func TestHostSetupJob_FailsWhenTheRootStageDidNotTurnLingerOn(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	require.NoError(t, h.root.Remove(h.rel(filepath.Join(h.state, "linger-builder"))))

	out, err := h.setup()

	require.Error(t, err)
	assert.Contains(t, out, "linger is off for builder. Run the root stage first")
	assert.Empty(t, rootActions(h.read(h.log)), "the job stage does not turn linger on: it has no sudo")
}

func TestHostSetupJob_RefusesAUserThatCanUseSudo(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup("FAKE_SUDO_RC=0")

	require.Error(t, err)
	assert.Contains(t, out, "the user "+"builder"+" can run sudo without a password, so a step of a gate can become root and take the token. Run the job stage as the job user "+jobUser)
	assert.NotContains(t, out, done)
	assert.NotContains(t, h.read(h.log), "make ", "the refusal comes before the code of the clone runs")
}

func TestHostSetupJob_ChecksAgainAtTheEndThatTheUserHasNoSudo(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup("FAKE_GRANT_SUDO=1")

	require.Error(t, err)
	assert.Contains(t, out, "the user builder can run sudo without a password")
	assert.Contains(t, h.read(h.log), "make setup", "the first check passed: the grant came during the run")
	assert.NotContains(t, out, done)
	assert.NoFileExists(t, filepath.Join(h.workRoot, ".beehave/host-setup.record"), "a record that says sudo=none is never written for a user that has sudo")
}

func TestHostSetupJob_RefusesAUserInARootGroup(t *testing.T) {
	t.Parallel()

	for _, group := range rootGroups {
		t.Run(group, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.setup("FAKE_GROUPS=builder " + group)

			require.Error(t, err)
			assert.Contains(t, out, "the user builder is in the group "+group+", which gives root")
			assert.NotContains(t, out, done)
			assert.NotContains(t, h.read(h.log), "make ", "the refusal comes before the code of the clone runs")
		})
	}
}

func TestHostSetupJob_FailsWhenTheJobUserGetsAnAnswerOnAnyMetadataPort(t *testing.T) {
	t.Parallel()

	const (
		tls4 = "https://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/token"
		tls6 = "https://[fd20:ce::254]/computeMetadata/v1/instance/service-accounts/default/token"
	)

	cases := []struct{ env, message string }{
		{env: "FAKE_METADATA_USER_CODE=200 FAKE_METADATA_USER_RC=0", message: "the job user got HTTP 200 from " + metadataURL4},
		{env: "FAKE_METADATA_USER6_CODE=403 FAKE_METADATA_USER6_RC=0", message: "the job user got HTTP 403 from " + metadataURL6},
		{env: "FAKE_METADATA_TLS_CODE=200 FAKE_METADATA_TLS_RC=0", message: "the job user got HTTP 200 from " + tls4},
		{env: "FAKE_METADATA_TLS6_CODE=000 FAKE_METADATA_TLS6_RC=35", message: "the check of " + tls6 + " ended with curl exit code 35, which proves nothing"},
		{env: "FAKE_METADATA_USER_RC=6", message: "ended with curl exit code 6, which proves nothing"},
	}

	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.setup(strings.Fields(tc.env)...)

			require.Error(t, err)
			assert.Contains(t, out, tc.message)
			assert.NotContains(t, out, done)
		})
	}
}

func TestHostSetupJob_RecordsTheRefusalAndTheMissingSudo(t *testing.T) {
	t.Parallel()

	h := newFakeHost(t)
	out, err := h.setup("FAKE_METADATA_USER_RC=28")

	require.NoError(t, err, out)
	assert.Contains(t, h.record(), "metadata user=refused sudo=none\n", "a timeout is no answer")
}

func TestHostSetup_RefusesAStageThatIsNotNamed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		args    []string
		message string
	}{
		{name: "no_stage", args: []string{"--watchdog", watchdogUnit}, message: "--stage is required"},
		{name: "an_unknown_stage", args: []string{"--stage", "both"}, message: "the stage both is not root or job"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newFakeHost(t)
			out, err := h.sh(nil, filepath.Join(h.clone, "scripts/host-setup.sh"), tc.args...)

			require.Error(t, err)
			assert.Contains(t, out, tc.message)
			assert.Empty(t, strings.TrimSpace(h.read(h.log)), "the refusal comes before any call")
		})
	}
}

func TestHostTools_RefusesAStageThatIsNotNamed(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{nil, {"both"}} {
		h := newFakeHost(t)
		out, err := h.sh(nil, filepath.Join(h.clone, "scripts/host-tools.sh"), args...)

		require.Error(t, err)
		assert.Contains(t, out, "name the stage: sh scripts/host-tools.sh system")
		assert.NotContains(t, h.read(h.log), "apt-get", "the refusal comes before any change")
	}
}
