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

// runGit runs a git command in dir, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) {
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

// writeFile writes name (creating any needed parent directories) under
// dir, failing the test on error.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newTestRepo creates a small Git repository with two commits:
//
//	r1: README.md, trunk/main.go, trunk/sub/nested.txt
//	r2: README.md changed
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	runGit(t, dir, "init", "-q", "-b", "main", ".")
	writeFile(t, dir, "README.md", "hello world\n")
	writeFile(t, dir, "trunk/main.go", "package main\n")
	writeFile(t, dir, "trunk/sub/nested.txt", "nested\n")
	runGit(t, dir, "add", "README.md", "trunk")
	runGit(t, dir, "commit", "-q", "-m", "initial commit")

	writeFile(t, dir, "README.md", "hello world, v2\n")
	runGit(t, dir, "commit", "-q", "-am", "update README")

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

func TestBareLookingRepoDirectory(t *testing.T) {
	requireTools(t)
	bin := buildBinary(t)

	// A repository directory conventionally ends in ".git" (especially a
	// bare one), but spelling that out in every SVN URL would be odd, so
	// gitrepo.FindRepo also tries that suffix when the bare path doesn't
	// exist -- this exercises it through a real "svn" client, the URL
	// omitting the ".git" suffix that's actually on disk.
	parent := t.TempDir()
	repoDir := filepath.Join(parent, "myrepo.git")
	if err := os.Mkdir(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "init", "-q", "-b", "main", ".")
	writeFile(t, repoDir, "README.md", "hello\n")
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-q", "-m", "initial commit")

	urlPath := filepath.Join(parent, "myrepo") // no ".git" suffix
	out, err := runSVNAgainst(t, bin, urlPath, "info")
	if err != nil {
		t.Fatalf("svn info: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Revision: 1") {
		t.Errorf("info output missing revision 1:\n%s", out)
	}

	out, err = runSVNAgainst(t, bin, filepath.Join(urlPath, "README.md"), "cat")
	if err != nil {
		t.Fatalf("svn cat: %v\n%s", err, out)
	}
	if out != "hello\n" {
		t.Errorf("cat output = %q, want %q", out, "hello\n")
	}
}

func TestLs(t *testing.T) {
	requireTools(t)
	bin := buildBinary(t)
	repo := newTestRepo(t)

	t.Run("root", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, repo, "ls")
		if err != nil {
			t.Fatalf("svn ls: %v\n%s", err, out)
		}
		if !strings.Contains(out, "README.md") || !strings.Contains(out, "trunk/") {
			t.Errorf("ls output missing expected entries:\n%s", out)
		}
	})

	t.Run("subdirectory", func(t *testing.T) {
		// This is the exact shape that used to crash a real svn client:
		// an earlier version of github.com/cespedes/svn reconstructed
		// each entry's wire path from the query path instead of trusting
		// the one the server callback gave it, which came out wrong (and
		// so triggered a segfault) whenever the client's session was
		// anchored below the repository root, as it is here.
		out, err := runSVNAgainst(t, bin, filepath.Join(repo, "trunk"), "ls")
		if err != nil {
			t.Fatalf("svn ls trunk: %v\n%s", err, out)
		}
		if !strings.Contains(out, "main.go") || !strings.Contains(out, "sub/") {
			t.Errorf("ls trunk output missing expected entries:\n%s", out)
		}
	})

	t.Run("nested subdirectory", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, filepath.Join(repo, "trunk", "sub"), "ls")
		if err != nil {
			t.Fatalf("svn ls trunk/sub: %v\n%s", err, out)
		}
		if !strings.Contains(out, "nested.txt") {
			t.Errorf("ls trunk/sub output missing nested.txt:\n%s", out)
		}
	})

	t.Run("on a file", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, filepath.Join(repo, "README.md"), "ls")
		if err == nil {
			t.Fatalf("svn ls on a file should fail:\n%s", out)
		}
	})
}

func TestLog(t *testing.T) {
	requireTools(t)
	bin := buildBinary(t)
	repo := newTestRepo(t)

	t.Run("plain log, newest first", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, repo, "log")
		if err != nil {
			t.Fatalf("svn log: %v\n%s", err, out)
		}
		iInitial := strings.Index(out, "initial commit")
		iUpdate := strings.Index(out, "update README")
		if iInitial < 0 || iUpdate < 0 {
			t.Fatalf("log output missing expected messages:\n%s", out)
		}
		if iUpdate > iInitial {
			t.Errorf("log should list r2 (update README) before r1 (initial commit):\n%s", out)
		}
		if !strings.Contains(out, "Tester") {
			t.Errorf("log output missing author:\n%s", out)
		}
	})

	t.Run("revision range", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, repo, "log", "-r1:1")
		if err != nil {
			t.Fatalf("svn log -r1:1: %v\n%s", err, out)
		}
		if !strings.Contains(out, "initial commit") {
			t.Errorf("log -r1:1 output missing r1's message:\n%s", out)
		}
		if strings.Contains(out, "update README") {
			t.Errorf("log -r1:1 should not include r2:\n%s", out)
		}
	})

	t.Run("path filtering", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, filepath.Join(repo, "trunk"), "log")
		if err != nil {
			t.Fatalf("svn log trunk: %v\n%s", err, out)
		}
		if !strings.Contains(out, "initial commit") {
			t.Errorf("log trunk output missing r1's message:\n%s", out)
		}
		if strings.Contains(out, "update README") {
			t.Errorf("log trunk should not include r2 (only README.md changed):\n%s", out)
		}
	})

	t.Run("verbose: changed paths", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, repo, "log", "-v", "-r1")
		if err != nil {
			t.Fatalf("svn log -v -r1: %v\n%s", err, out)
		}
		for _, want := range []string{"A /README.md", "A /trunk", "A /trunk/main.go", "A /trunk/sub", "A /trunk/sub/nested.txt"} {
			if !strings.Contains(out, want) {
				t.Errorf("log -v -r1 output missing %q:\n%s", want, out)
			}
		}
	})
}

// checkout runs "svn checkout" of repoPath into a fresh temporary
// directory, tunneled the same way runSVNAgainst is, and returns that
// directory.
func checkout(t *testing.T, bin, repoPath string, args ...string) string {
	t.Helper()
	wc := t.TempDir()
	url := "svn+gitsvnservertest://localhost" + repoPath
	fullArgs := append([]string{
		"checkout", "--non-interactive",
		"--config-option=config:tunnels:gitsvnservertest=" + bin,
	}, args...)
	fullArgs = append(fullArgs, url, wc)
	if out, err := exec.Command("svn", fullArgs...).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout: %v\n%s", err, out)
	}
	return wc
}

func TestCheckout(t *testing.T) {
	requireTools(t)
	bin := buildBinary(t)
	repo := newTestRepo(t)

	t.Run("full checkout at HEAD", func(t *testing.T) {
		wc := checkout(t, bin, repo)
		for name, want := range map[string]string{
			"README.md":            "hello world, v2\n",
			"trunk/main.go":        "package main\n",
			"trunk/sub/nested.txt": "nested\n",
		} {
			got, err := os.ReadFile(filepath.Join(wc, name))
			if err != nil {
				t.Errorf("reading %s: %v", name, err)
				continue
			}
			if string(got) != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}

		out, err := exec.Command("svn", "status", wc).CombinedOutput()
		if err != nil {
			t.Fatalf("svn status: %v\n%s", err, out)
		}
		if len(out) != 0 {
			t.Errorf("svn status on a fresh checkout should be clean, got:\n%s", out)
		}
	})

	t.Run("checkout at an older revision", func(t *testing.T) {
		wc := checkout(t, bin, repo, "-r1")
		got, err := os.ReadFile(filepath.Join(wc, "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "hello world\n" {
			t.Errorf("README.md = %q, want r1's content %q", got, "hello world\n")
		}
	})

	t.Run("checkout of a subdirectory", func(t *testing.T) {
		// Regression test: main.go used to resolve the session's base
		// path (here "trunk") against the repository root itself
		// before calling svn.Server.CheckoutEdit, which then resolved
		// it *again* internally (via its own List/GetFile calls, the
		// same ones every other command already goes through) --
		// ending up looking for "trunk/trunk" and failing every time
		// the session wasn't anchored at the repository root.
		wc := checkout(t, bin, filepath.Join(repo, "trunk"))
		got, err := os.ReadFile(filepath.Join(wc, "main.go"))
		if err != nil {
			t.Fatalf("reading main.go: %v", err)
		}
		if string(got) != "package main\n" {
			t.Errorf("main.go = %q, want %q", got, "package main\n")
		}
		if _, err := os.Stat(filepath.Join(wc, "sub", "nested.txt")); err != nil {
			t.Errorf("sub/nested.txt missing from the checkout: %v", err)
		}
		if _, err := os.Stat(filepath.Join(wc, "README.md")); err == nil {
			t.Error("README.md should not be part of a checkout of trunk alone")
		}
	})

	t.Run("svn update applies real changes", func(t *testing.T) {
		wc := checkout(t, bin, repo)

		runGit(t, repo, "rm", "-q", "-r", "trunk/sub")
		writeFile(t, repo, "README.md", "hello world, v3\n")
		writeFile(t, repo, "newfile.txt", "brand new\n")
		runGit(t, repo, "add", "README.md", "newfile.txt")
		runGit(t, repo, "commit", "-q", "-am", "v3: drop trunk/sub, add newfile.txt")

		cmd := exec.Command("svn", "update", "--non-interactive",
			"--config-option=config:tunnels:gitsvnservertest="+bin, wc)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("svn update: %v\n%s", err, out)
		}

		got, err := os.ReadFile(filepath.Join(wc, "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "hello world, v3\n" {
			t.Errorf("README.md = %q, want %q", got, "hello world, v3\n")
		}
		if _, err := os.ReadFile(filepath.Join(wc, "newfile.txt")); err != nil {
			t.Errorf("newfile.txt missing after update: %v", err)
		}
		if _, err := os.Stat(filepath.Join(wc, "trunk", "sub")); err == nil {
			t.Error("trunk/sub should have been removed by the update")
		}

		out, err := exec.Command("svn", "status", wc).CombinedOutput()
		if err != nil {
			t.Fatalf("svn status: %v\n%s", err, out)
		}
		if len(out) != 0 {
			t.Errorf("svn status after a clean update should be empty, got:\n%s", out)
		}
	})

	t.Run("svn update on a subdirectory checkout", func(t *testing.T) {
		wc := checkout(t, bin, filepath.Join(repo, "trunk"))

		writeFile(t, repo, "trunk/main.go", "package main // v2\n")
		runGit(t, repo, "commit", "-q", "-am", "v2: change trunk/main.go")

		cmd := exec.Command("svn", "update", "--non-interactive",
			"--config-option=config:tunnels:gitsvnservertest="+bin, wc)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("svn update: %v\n%s", err, out)
		}
		got, err := os.ReadFile(filepath.Join(wc, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "package main // v2\n" {
			t.Errorf("main.go = %q, want %q", got, "package main // v2\n")
		}
	})
}

// runSVNOn runs "svn" with args as-is (unlike runSVNAgainst, which builds
// and appends a URL itself), tunneled the same way -- for commands like
// "diff" that take a working-copy path rather than a URL.
func runSVNOn(t *testing.T, bin string, args ...string) (string, error) {
	t.Helper()
	fullArgs := append([]string{
		"--non-interactive",
		"--config-option=config:tunnels:gitsvnservertest=" + bin,
	}, args...)
	out, err := exec.Command("svn", fullArgs...).CombinedOutput()
	return string(out), err
}

func TestDiff(t *testing.T) {
	requireTools(t)
	bin := buildBinary(t)
	repo := newTestRepo(t)
	// r3: trunk/sub/nested.txt changed, so there's a nested file to diff
	// too (neither newTestRepo commit touches it after r1).
	writeFile(t, repo, "trunk/sub/nested.txt", "nested, v2\n")
	runGit(t, repo, "commit", "-q", "-am", "update nested.txt")

	t.Run("whole repository, repo-to-repo", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, repo, "diff", "-r1:2")
		if err != nil {
			t.Fatalf("svn diff: %v\n%s", err, out)
		}
		if !strings.Contains(out, "Index: README.md") || !strings.Contains(out, "+hello world, v2") {
			t.Errorf("diff output missing README.md's change:\n%s", out)
		}
	})

	t.Run("a single flat file, repo-to-repo", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, filepath.Join(repo, "README.md"), "diff", "-r1:2")
		if err != nil {
			t.Fatalf("svn diff: %v\n%s", err, out)
		}
		if !strings.Contains(out, "+hello world, v2") {
			t.Errorf("diff output missing README.md's change:\n%s", out)
		}
	})

	t.Run("a nested file, repo-to-repo", func(t *testing.T) {
		// Regression test: UpdateEdit (which "diff" reuses under the
		// hood, same as "update") used to assume its path argument was
		// always the node being described; a plain file nested more
		// than one level below the session's own anchor needs it
		// described as a separate "target" instead, since the Editor
		// Command Set has no way to open a node that isn't a directory.
		out, err := runSVNAgainst(t, bin, filepath.Join(repo, "trunk", "sub", "nested.txt"), "diff", "-r1:3")
		if err != nil {
			t.Fatalf("svn diff: %v\n%s", err, out)
		}
		if !strings.Contains(out, "+nested, v2") {
			t.Errorf("diff output missing nested.txt's change:\n%s", out)
		}
	})

	t.Run("from a working copy, against an older revision", func(t *testing.T) {
		wc := checkout(t, bin, repo)
		out, err := runSVNOn(t, bin, "diff", "-r1", filepath.Join(wc, "trunk", "sub", "nested.txt"))
		if err != nil {
			t.Fatalf("svn diff: %v\n%s", err, out)
		}
		if !strings.Contains(out, "+nested, v2") {
			t.Errorf("diff output missing nested.txt's change:\n%s", out)
		}
	})
}

func TestCat(t *testing.T) {
	requireTools(t)
	bin := buildBinary(t)
	repo := newTestRepo(t)

	t.Run("latest revision", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, filepath.Join(repo, "README.md"), "cat")
		if err != nil {
			t.Fatalf("svn cat: %v\n%s", err, out)
		}
		if out != "hello world, v2\n" {
			t.Errorf("cat output = %q, want %q", out, "hello world, v2\n")
		}
	})

	t.Run("on a directory", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, filepath.Join(repo, "trunk"), "cat")
		if err == nil {
			t.Fatalf("svn cat on a directory should fail:\n%s", out)
		}
	})

	t.Run("nonexistent path", func(t *testing.T) {
		out, err := runSVNAgainst(t, bin, filepath.Join(repo, "does-not-exist"), "cat")
		if err == nil {
			t.Fatalf("svn cat on a nonexistent path should fail:\n%s", out)
		}
	})
}
