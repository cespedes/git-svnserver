package gitrepo_test

// Unit tests for the gitrepo package, calling it directly (as opposed to
// main_test.go's black-box tests, which drive a separately-built binary
// through a real "svn" client and so aren't visible to "go test -cover").

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cespedes/svn"

	"github.com/cespedes/git-svnserver/gitrepo"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not found in PATH; skipping")
	}
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

// newTestRepo creates a Git repository with three commits:
//
//	r1: README.md ("hello\n"), trunk/main.go, trunk/sub/nested.txt
//	r2: README.md changed
//	r3: trunk/main.go changed
//
// so trunk/sub/nested.txt is only ever touched at r1, README.md was last
// touched at r2, and trunk/main.go at r3.
func newTestRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()

	runGit(t, dir, "init", "-q", "-b", "main", ".")
	writeFile(t, dir, "README.md", "hello\n")
	writeFile(t, dir, "trunk/main.go", "package main\n")
	writeFile(t, dir, "trunk/sub/nested.txt", "nested\n")
	runGit(t, dir, "add", "README.md", "trunk")
	runGit(t, dir, "commit", "-q", "-m", "initial commit")

	writeFile(t, dir, "README.md", "hello, v2\n")
	runGit(t, dir, "commit", "-q", "-am", "update README")

	writeFile(t, dir, "trunk/main.go", "package main\n\nfunc main() {}\n")
	runGit(t, dir, "commit", "-q", "-am", "flesh out main.go")

	return dir
}

func revMapPath(repoDir string) string {
	return filepath.Join(repoDir, ".git", "svnserver-revmap")
}

func mustFindRepo(t *testing.T, path string) (*gitrepo.Repo, string) {
	t.Helper()
	r, sub, err := gitrepo.FindRepo(path)
	if err != nil {
		t.Fatalf("FindRepo(%q): %v", path, err)
	}
	return r, sub
}

func TestFindRepo(t *testing.T) {
	dir := newTestRepo(t)

	t.Run("at the repository root", func(t *testing.T) {
		_, sub := mustFindRepo(t, dir)
		if sub != "" {
			t.Errorf("subPath = %q, want empty", sub)
		}
	})

	t.Run("walks up from a file inside the repository", func(t *testing.T) {
		_, sub := mustFindRepo(t, filepath.Join(dir, "trunk", "main.go"))
		if sub != filepath.Join("trunk", "main.go") {
			t.Errorf("subPath = %q, want %q", sub, filepath.Join("trunk", "main.go"))
		}
	})

	t.Run("walks up from a subdirectory", func(t *testing.T) {
		_, sub := mustFindRepo(t, filepath.Join(dir, "trunk", "sub"))
		if sub != filepath.Join("trunk", "sub") {
			t.Errorf("subPath = %q, want %q", sub, filepath.Join("trunk", "sub"))
		}
	})

	t.Run("no repository anywhere in the ancestry", func(t *testing.T) {
		outside := t.TempDir()
		_, _, err := gitrepo.FindRepo(outside)
		if err == nil {
			t.Fatalf("FindRepo(%q): want an error, got none", outside)
		}
	})
}

func TestLatestRev(t *testing.T) {
	dir := newTestRepo(t)
	r, _ := mustFindRepo(t, dir)
	if got, want := r.LatestRev(), uint(3); got != want {
		t.Errorf("LatestRev() = %d, want %d", got, want)
	}
}

func TestUUID(t *testing.T) {
	dir := newTestRepo(t)
	r1, _ := mustFindRepo(t, dir)
	r2, _ := mustFindRepo(t, dir)
	if r1.UUID() == "" {
		t.Fatal("UUID() is empty")
	}
	if r1.UUID() != r2.UUID() {
		t.Errorf("UUID() is not stable across separate opens of the same repository: %q != %q", r1.UUID(), r2.UUID())
	}
}

func TestStat(t *testing.T) {
	dir := newTestRepo(t)
	r, _ := mustFindRepo(t, dir)

	t.Run("root", func(t *testing.T) {
		d, err := r.Stat("", nil)
		if err != nil {
			t.Fatalf("Stat(\"\"): %v", err)
		}
		if d.Kind != "dir" {
			t.Errorf("Kind = %q, want %q", d.Kind, "dir")
		}
	})

	t.Run("file last changed before the latest revision", func(t *testing.T) {
		d, err := r.Stat("README.md", nil)
		if err != nil {
			t.Fatalf("Stat(\"README.md\"): %v", err)
		}
		if d.Kind != "file" {
			t.Errorf("Kind = %q, want %q", d.Kind, "file")
		}
		if d.CreatedRev != 2 {
			t.Errorf("CreatedRev = %d, want 2", d.CreatedRev)
		}
		if d.LastAuthor != "Tester" {
			t.Errorf("LastAuthor = %q, want %q", d.LastAuthor, "Tester")
		}
	})

	t.Run("file last changed at the latest revision", func(t *testing.T) {
		d, err := r.Stat("trunk/main.go", nil)
		if err != nil {
			t.Fatalf("Stat(\"trunk/main.go\"): %v", err)
		}
		if d.CreatedRev != 3 {
			t.Errorf("CreatedRev = %d, want 3", d.CreatedRev)
		}
	})

	t.Run("directory unchanged since r1", func(t *testing.T) {
		d, err := r.Stat("trunk/sub", nil)
		if err != nil {
			t.Fatalf("Stat(\"trunk/sub\"): %v", err)
		}
		if d.Kind != "dir" {
			t.Errorf("Kind = %q, want %q", d.Kind, "dir")
		}
		if d.CreatedRev != 1 {
			t.Errorf("CreatedRev = %d, want 1", d.CreatedRev)
		}
	})

	t.Run("at an older revision", func(t *testing.T) {
		one := uint(1)
		d, err := r.Stat("README.md", &one)
		if err != nil {
			t.Fatalf("Stat(\"README.md\", r1): %v", err)
		}
		if d.CreatedRev != 1 {
			t.Errorf("CreatedRev = %d, want 1 (README.md didn't change again until r2)", d.CreatedRev)
		}
	})

	t.Run("nonexistent path", func(t *testing.T) {
		_, err := r.Stat("does-not-exist", nil)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("error = %v, want errors.Is(err, fs.ErrNotExist)", err)
		}
	})

	t.Run("path that only exists at a later revision", func(t *testing.T) {
		one := uint(1)
		_, err := r.Stat("trunk/main.go", &one)
		if err != nil {
			t.Fatalf("trunk/main.go already exists at r1, Stat should succeed: %v", err)
		}
	})
}

func TestCheckPath(t *testing.T) {
	dir := newTestRepo(t)
	r, _ := mustFindRepo(t, dir)

	cases := []struct {
		path string
		want string
	}{
		{"", "dir"},
		{"README.md", "file"},
		{"trunk", "dir"},
		{"does-not-exist", "none"},
	}
	for _, c := range cases {
		got, err := r.CheckPath(c.path, nil)
		if err != nil {
			t.Errorf("CheckPath(%q): %v", c.path, err)
			continue
		}
		if got != c.want {
			t.Errorf("CheckPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestList(t *testing.T) {
	dir := newTestRepo(t)
	r, _ := mustFindRepo(t, dir)

	// direntsByPath runs List and indexes the result by its full,
	// slash-prefixed Path, failing the test if any path appears twice.
	direntsByPath := func(path string) map[string]uint {
		t.Helper()
		dirents, err := r.List(path, nil)
		if err != nil {
			t.Fatalf("List(%q): %v", path, err)
		}
		byPath := make(map[string]uint, len(dirents))
		for _, d := range dirents {
			if _, dup := byPath[d.Path]; dup {
				t.Fatalf("List(%q): duplicate entry for %q", path, d.Path)
			}
			byPath[d.Path] = d.CreatedRev
		}
		return byPath
	}

	t.Run("root includes an entry for itself", func(t *testing.T) {
		got := direntsByPath("")
		// "/" itself stays at r1: its own direct entries (README.md,
		// trunk) never change afterwards, even though r2 and r3 each
		// change something nested inside one of them.
		want := map[string]uint{"/": 1, "/README.md": 2, "/trunk": 1}
		for path, wantRev := range want {
			gotRev, ok := got[path]
			if !ok {
				t.Errorf("missing entry for %q (got: %v)", path, got)
				continue
			}
			if gotRev != wantRev {
				t.Errorf("CreatedRev for %q = %d, want %d", path, gotRev, wantRev)
			}
		}
		if len(got) != len(want) {
			t.Errorf("List(\"\") = %v, want exactly %v", got, want)
		}
	})

	t.Run("subdirectory: full paths, not bare base names", func(t *testing.T) {
		// This is the shape a real "svn ls" on a subdirectory needs: an
		// earlier version of github.com/cespedes/svn's Server.List
		// reconstructed paths from the query path instead of trusting
		// these, and got it wrong for exactly this case (a segfault in
		// a real svn client, confirmed against a real svnserve).
		got := direntsByPath("trunk")
		want := map[string]uint{"/trunk": 1, "/trunk/main.go": 3, "/trunk/sub": 1}
		for path, wantRev := range want {
			if gotRev, ok := got[path]; !ok {
				t.Errorf("missing entry for %q (got: %v)", path, got)
			} else if gotRev != wantRev {
				t.Errorf("CreatedRev for %q = %d, want %d", path, gotRev, wantRev)
			}
		}
		if len(got) != len(want) {
			t.Errorf("List(\"trunk\") = %v, want exactly %v", got, want)
		}
	})

	t.Run("on a file", func(t *testing.T) {
		if _, err := r.List("README.md", nil); err == nil {
			t.Error("List(\"README.md\"): want an error, README.md is not a directory")
		}
	})

	t.Run("nonexistent path", func(t *testing.T) {
		_, err := r.List("does-not-exist", nil)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("error = %v, want errors.Is(err, fs.ErrNotExist)", err)
		}
	})
}

func TestGetFile(t *testing.T) {
	dir := newTestRepo(t)
	r, _ := mustFindRepo(t, dir)

	t.Run("content and created-rev at the latest revision", func(t *testing.T) {
		createdRev, content, err := r.GetFile("README.md", nil, true)
		if err != nil {
			t.Fatalf("GetFile: %v", err)
		}
		if string(content) != "hello, v2\n" {
			t.Errorf("content = %q, want %q", content, "hello, v2\n")
		}
		if createdRev != 2 {
			t.Errorf("createdRev = %d, want 2", createdRev)
		}
	})

	t.Run("content at an older revision", func(t *testing.T) {
		one := uint(1)
		createdRev, content, err := r.GetFile("README.md", &one, true)
		if err != nil {
			t.Fatalf("GetFile: %v", err)
		}
		if string(content) != "hello\n" {
			t.Errorf("content = %q, want %q", content, "hello\n")
		}
		if createdRev != 1 {
			t.Errorf("createdRev = %d, want 1", createdRev)
		}
	})

	t.Run("without contents", func(t *testing.T) {
		createdRev, content, err := r.GetFile("README.md", nil, false)
		if err != nil {
			t.Fatalf("GetFile: %v", err)
		}
		if content != nil {
			t.Errorf("content = %q, want nil (wantContents was false)", content)
		}
		if createdRev != 2 {
			t.Errorf("createdRev = %d, want 2", createdRev)
		}
	})

	t.Run("on a directory", func(t *testing.T) {
		if _, _, err := r.GetFile("trunk", nil, true); err == nil {
			t.Error("GetFile(\"trunk\"): want an error, trunk is a directory")
		}
	})

	t.Run("nonexistent path", func(t *testing.T) {
		_, _, err := r.GetFile("does-not-exist", nil, true)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("error = %v, want errors.Is(err, fs.ErrNotExist)", err)
		}
	})
}

func TestLog(t *testing.T) {
	dir := newTestRepo(t)
	r, _ := mustFindRepo(t, dir)

	revs := func(entries []svn.LogEntry) []uint {
		out := make([]uint, len(entries))
		for i, e := range entries {
			out[i] = e.Rev
		}
		return out
	}
	eq := func(t *testing.T, got, want []uint) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}

	t.Run("startRev 0 means the latest revision, descending order", func(t *testing.T) {
		entries, err := r.Log(nil, 0, 0, false)
		if err != nil {
			t.Fatalf("Log: %v", err)
		}
		eq(t, revs(entries), []uint{3, 2, 1})
		if entries[0].Message != "flesh out main.go" {
			t.Errorf("r3 message = %q, want %q", entries[0].Message, "flesh out main.go")
		}
		if entries[0].Author != "Tester" {
			t.Errorf("r3 author = %q, want %q", entries[0].Author, "Tester")
		}
		if entries[0].Changed != nil {
			t.Errorf("Changed = %v, want nil (changedPaths was false)", entries[0].Changed)
		}
	})

	t.Run("ascending when startRev < endRev", func(t *testing.T) {
		entries, err := r.Log(nil, 1, 2, false)
		if err != nil {
			t.Fatalf("Log: %v", err)
		}
		eq(t, revs(entries), []uint{1, 2})
	})

	t.Run("descending when startRev > endRev", func(t *testing.T) {
		entries, err := r.Log(nil, 2, 1, false)
		if err != nil {
			t.Fatalf("Log: %v", err)
		}
		eq(t, revs(entries), []uint{2, 1})
	})

	t.Run("filtered to revisions that touched a file", func(t *testing.T) {
		entries, err := r.Log([]string{"README.md"}, 3, 0, false)
		if err != nil {
			t.Fatalf("Log: %v", err)
		}
		eq(t, revs(entries), []uint{2, 1})
	})

	t.Run("filtered to revisions that touched anything under a directory", func(t *testing.T) {
		entries, err := r.Log([]string{"trunk"}, 3, 0, false)
		if err != nil {
			t.Fatalf("Log: %v", err)
		}
		// r2 only changed README.md, not anything under trunk.
		eq(t, revs(entries), []uint{3, 1})
	})

	t.Run("changed paths", func(t *testing.T) {
		entries, err := r.Log(nil, 3, 0, true)
		if err != nil {
			t.Fatalf("Log: %v", err)
		}
		byRev := map[uint][]svn.ChangedPath{}
		for _, e := range entries {
			byRev[e.Rev] = e.Changed
		}

		changedPaths := func(cs []svn.ChangedPath) map[string]string {
			m := make(map[string]string, len(cs))
			for _, c := range cs {
				m[c.Path] = c.Mode
			}
			return m
		}

		t.Run("r1 also reports the directories it created", func(t *testing.T) {
			got := changedPaths(byRev[1])
			want := map[string]string{
				"/README.md": "A", "/trunk": "A", "/trunk/main.go": "A",
				"/trunk/sub": "A", "/trunk/sub/nested.txt": "A",
			}
			if len(got) != len(want) {
				t.Fatalf("Changed = %v, want %v", got, want)
			}
			for path, mode := range want {
				if got[path] != mode {
					t.Errorf("Changed[%q] = %q, want %q", path, got[path], mode)
				}
			}
		})

		t.Run("r2 only touches README.md, no directories", func(t *testing.T) {
			got := changedPaths(byRev[2])
			want := map[string]string{"/README.md": "M"}
			if len(got) != len(want) || got["/README.md"] != "M" {
				t.Errorf("Changed = %v, want %v", got, want)
			}
		})

		t.Run("r3 only touches trunk/main.go, trunk already existed", func(t *testing.T) {
			got := changedPaths(byRev[3])
			want := map[string]string{"/trunk/main.go": "M"}
			if len(got) != len(want) || got["/trunk/main.go"] != "M" {
				t.Errorf("Changed = %v, want %v", got, want)
			}
		})

		first := byRev[1][0]
		if first.Info == nil || first.Info.NodeKind == "" {
			t.Errorf("Changed[0].Info = %v, want a populated NodeKind", first.Info)
		}
	})

	t.Run("out of range revision", func(t *testing.T) {
		if _, err := r.Log(nil, 100, 0, false); err == nil {
			t.Error("Log with startRev beyond the latest revision: want an error")
		}
	})
}

func TestRevMapPersistsAndIsStableAcrossOpens(t *testing.T) {
	dir := newTestRepo(t)
	mustFindRepo(t, dir)

	data, err := os.ReadFile(revMapPath(dir))
	if err != nil {
		t.Fatalf("revision map was not created: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("revision map has %d lines, want 3 (one per commit): %q", len(lines), data)
	}

	// Opening the same repository again, with no new commits, must not
	// rewrite the file.
	mustFindRepo(t, dir)
	data2, err := os.ReadFile(revMapPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(data2) {
		t.Errorf("revision map changed with no new commits:\nbefore: %q\nafter:  %q", data, data2)
	}
}

func TestRevMapExtendsOnNewCommits(t *testing.T) {
	dir := newTestRepo(t)
	r, _ := mustFindRepo(t, dir)
	before := r.LatestRev()

	writeFile(t, dir, "trunk/main.go", "package main\n\nfunc main() { println(\"hi\") }\n")
	runGit(t, dir, "commit", "-q", "-am", "one more commit")

	before2, err := os.ReadFile(revMapPath(dir))
	if err != nil {
		t.Fatal(err)
	}

	r2, _ := mustFindRepo(t, dir)
	if got, want := r2.LatestRev(), before+1; got != want {
		t.Errorf("LatestRev() after a new commit = %d, want %d", got, want)
	}

	after2, err := os.ReadFile(revMapPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(after2), string(before2)) {
		t.Errorf("extending the revision map did not preserve its existing prefix:\nbefore: %q\nafter:  %q", before2, after2)
	}

	d, err := r2.Stat("trunk/main.go", nil)
	if err != nil {
		t.Fatalf("Stat(\"trunk/main.go\"): %v", err)
	}
	if d.CreatedRev != before+1 {
		t.Errorf("CreatedRev = %d, want %d", d.CreatedRev, before+1)
	}
}

func TestRevMapRebuildsOnRewrittenHistory(t *testing.T) {
	dir := newTestRepo(t)
	r, _ := mustFindRepo(t, dir)
	if r.LatestRev() != 3 {
		t.Fatalf("LatestRev() = %d, want 3", r.LatestRev())
	}

	// Rewrite history: reset back to r1 and commit something different in
	// r2's place, so the old r2/r3 commits are no longer reachable from
	// the branch tip -- the same shape of change a rebase or a
	// force-pushed branch produces.
	runGit(t, dir, "reset", "-q", "--hard", "HEAD~2")
	writeFile(t, dir, "README.md", "a completely different history\n")
	runGit(t, dir, "commit", "-q", "-am", "rewritten r2")

	r2, _ := mustFindRepo(t, dir)
	if got, want := r2.LatestRev(), uint(2); got != want {
		t.Errorf("LatestRev() after rewriting history = %d, want %d", got, want)
	}
	d, err := r2.Stat("README.md", nil)
	if err != nil {
		t.Fatalf("Stat(\"README.md\"): %v", err)
	}
	if d.CreatedRev != 2 {
		t.Errorf("CreatedRev = %d, want 2 (the rewritten commit)", d.CreatedRev)
	}
}

func TestEmptyRepository(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main", ".")

	r, _ := mustFindRepo(t, dir)
	if got := r.LatestRev(); got != 0 {
		t.Errorf("LatestRev() on an empty repository = %d, want 0", got)
	}
	// A repository with no commits yet has no root tree either: this is a
	// known, currently-accepted limitation (a real SVN repository always
	// has an r0 root directory, even a freshly created one), documented
	// here so it doesn't change silently.
	_, err := r.Stat("", nil)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat(\"\") on an empty repository: error = %v, want errors.Is(err, fs.ErrNotExist)", err)
	}
}
