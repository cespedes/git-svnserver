package main

// Integration tests that run a real "svn" client against this program, the
// same way server_integration_test.go in github.com/cespedes/svn tests the
// underlying library. They skip cleanly if "svn" or "git" aren't on PATH.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"svn", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found in PATH; skipping", tool)
		}
	}
}

// buildBinary compiles this program once per test binary run and returns
// the path to the resulting executable.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "git-svnserver")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// newTestRepo creates a small Git repository with two commits:
//
//	r1: README.md, trunk/main.go
//	r2: README.md changed
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
			"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, content string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-q", "-b", "main", ".")
	write("README.md", "hello world\n")
	write("trunk/main.go", "package main\n")
	run("add", "README.md", "trunk")
	run("commit", "-q", "-m", "initial commit")

	write("README.md", "hello world, v2\n")
	run("commit", "-q", "-am", "update README")

	return dir
}

// runSVNAgainst runs "svn" against repoPath, tunneled through the binary
// built by buildBinary, using svn's "tunnels" mechanism (the same
// indirection a real "svn+ssh://" URL uses to invoke "ssh ... svnserve
// -t", except here the "tunnel command" is this program itself, so no
// network or ssh is involved).
func runSVNAgainst(t *testing.T, bin, repoPath string, args ...string) (string, error) {
	t.Helper()
	url := "svn+gitsvnservertest://localhost" + repoPath
	fullArgs := append([]string{
		"--non-interactive",
		"--config-option=config:tunnels:gitsvnservertest=" + bin,
	}, args...)
	fullArgs = append(fullArgs, url)
	out, err := exec.Command("svn", fullArgs...).CombinedOutput()
	return string(out), err
}

func TestInfo(t *testing.T) {
	requireTools(t)
	bin := buildBinary(t)
	repo := newTestRepo(t)

	t.Run("root at HEAD", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, repo, "info")
		if err != nil {
			t.Fatalf("svn info: %v\n%s", err, out)
		}
		if !strings.Contains(out, "Revision: 2") {
			t.Errorf("info output missing revision 2:\n%s", out)
		}
		if !strings.Contains(out, "Node Kind: directory") {
			t.Errorf("info output missing directory kind:\n%s", out)
		}
		if !strings.Contains(out, "Tester") {
			t.Errorf("info output missing author:\n%s", out)
		}
	})

	t.Run("file, last changed at the revision that touched it", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, filepath.Join(repo, "README.md"), "info")
		if err != nil {
			t.Fatalf("svn info: %v\n%s", err, out)
		}
		if !strings.Contains(out, "Node Kind: file") {
			t.Errorf("info output missing file kind:\n%s", out)
		}
		if !strings.Contains(out, "Last Changed Rev: 2") {
			t.Errorf("README.md should be last changed at r2:\n%s", out)
		}
	})

	t.Run("directory only touched at r1", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, filepath.Join(repo, "trunk"), "info")
		if err != nil {
			t.Fatalf("svn info: %v\n%s", err, out)
		}
		if !strings.Contains(out, "Last Changed Rev: 1") {
			t.Errorf("trunk should be last changed at r1:\n%s", out)
		}
	})

	t.Run("older revision", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, repo, "info", "-r1")
		if err != nil {
			t.Fatalf("svn info -r1: %v\n%s", err, out)
		}
		if !strings.Contains(out, "Revision: 1") {
			t.Errorf("info -r1 output missing revision 1:\n%s", out)
		}
	})

	t.Run("nonexistent path", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, filepath.Join(repo, "does-not-exist"), "info")
		if err == nil {
			t.Fatalf("svn info on a nonexistent path should fail:\n%s", out)
		}
		if !strings.Contains(out, "E200009") && !strings.Contains(out, "non-existent") {
			t.Errorf("expected a \"path doesn't exist\" error, got:\n%s", out)
		}
	})

	t.Run("revision map file is created and stable across runs", func(t *testing.T) {
		mapFile := filepath.Join(repo, ".git", "svnserver-revmap")
		if _, err := os.Stat(mapFile); err != nil {
			t.Fatalf("revision map was not created: %v", err)
		}
		before, err := os.ReadFile(mapFile)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runSVNAgainst(t, bin, repo, "info"); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(mapFile)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Errorf("revision map changed with no new commits:\nbefore: %q\nafter:  %q", before, after)
		}
	})
}
