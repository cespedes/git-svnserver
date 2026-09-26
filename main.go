// Command git-svnserver behaves like "svnserve -t": it speaks the SVN
// tunnel protocol over stdin/stdout, the way a real svnserve does when
// invoked over ssh. Instead of a real SVN repository, though, it serves
// the content of a Git repository's main branch, mapping each of its
// commits to one SVN revision.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/cespedes/svn"

	"github.com/cespedes/git-svnserver/gitrepo"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("git-svnserver: ")

	tunnel := flag.Bool("t", false, "tunnel mode: speak the protocol over stdin/stdout (the only mode supported, kept for compatibility with how svnserve is invoked)")
	flag.Parse()
	_ = tunnel

	// run (i.e. svn.Server.Serve) always returns a non-nil error: io.EOF
	// once the connection ends cleanly, or a real failure otherwise.
	if err := run(os.Stdin, os.Stdout); !errors.Is(err, io.EOF) {
		log.Fatal(err)
	}
}

func run(in io.Reader, out io.Writer) error {
	// repo and sessionBase are filled in by Greet, once the client tells
	// us which path it wants; every other callback is only ever invoked
	// after that has happened.
	var repo *gitrepo.Repo
	var sessionBase string

	var server svn.Server

	server.Greet = func(version int, capabilities []string, connectURL string, raclient string, client *string) (svn.ReposInfo, error) {
		u, err := url.Parse(connectURL)
		if err != nil {
			return svn.ReposInfo{}, fmt.Errorf("parsing client URL %q: %w", connectURL, err)
		}
		repo, sessionBase, err = gitrepo.FindRepo(u.Path)
		if err != nil {
			return svn.ReposInfo{}, err
		}
		root := *u
		root.Path = strings.TrimSuffix(u.Path, sessionBase)
		return svn.ReposInfo{
			UUID:         repo.UUID(),
			URL:          root.String(),
			Capabilities: []string{},
		}, nil
	}

	server.GetLatestRev = func() (int, error) {
		return int(repo.LatestRev()), nil
	}

	server.Stat = func(path string, rev *uint) (svn.Dirent, error) {
		return repo.Stat(withBase(sessionBase, path), rev)
	}

	server.CheckPath = func(path string, rev *uint) (string, error) {
		return repo.CheckPath(withBase(sessionBase, path), rev)
	}

	server.List = func(reqPath string, rev *uint, depth string, fields []string, pattern []string) ([]svn.Dirent, error) {
		dirents, err := repo.List(withBase(sessionBase, reqPath), rev)
		if err != nil {
			return nil, err
		}
		return filterByPattern(dirents, pattern), nil
	}

	server.GetFile = func(reqPath string, rev *uint, wantProps bool, wantContents bool) (uint, []svn.PropList, []byte, error) {
		createdRev, content, err := repo.GetFile(withBase(sessionBase, reqPath), rev, wantContents)
		if err != nil {
			return 0, nil, nil, err
		}
		return createdRev, nil, content, nil
	}

	server.Log = func(reqPaths []string, startRev uint, endRev uint, changedPaths bool) ([]svn.LogEntry, error) {
		paths := make([]string, len(reqPaths))
		for i, p := range reqPaths {
			paths[i] = withBase(sessionBase, p)
		}
		return repo.Log(paths, startRev, endRev, changedPaths)
	}

	// updateRev/target/diffErr are filled in by Update or Diff, then read
	// back by FinishReport once the report that follows it is done -- see
	// FinishReport's own comment for why only these two report shapes are
	// handled, and UpdateEdit's doc comment (in github.com/cespedes/svn)
	// for why "update" and "diff" need to fill in target differently.
	var updateRev *uint
	var target string
	var diffErr error

	server.Update = func(rev *uint, t string, recurse bool) {
		updateRev = rev
		target = t
		diffErr = nil
	}

	server.Diff = func(rev *uint, t string, recurse bool, ignoreAncestry bool, versusURL string, textDeltas bool, depth string) {
		updateRev = rev
		diffErr = nil
		repoRelative, err := svn.RepoRelativePath(server.ReposInfo.URL, versusURL)
		if err != nil {
			diffErr = err
			return
		}
		target = stripBase(sessionBase, repoRelative)
	}

	server.FinishReport = func(report []svn.ReportedPath) ([]svn.Item, error) {
		if diffErr != nil {
			return nil, diffErr
		}
		// A "mixed-revision" working copy (part of it pinned to an older
		// revision than the rest, e.g. via "svn update -r") reports more
		// than one entry, or a non-root one; svn.UpdateEdit only handles
		// the common case of a client entirely at one revision, which
		// (like a plain checkout) always reduces to exactly one entry.
		if len(report) != 1 {
			return nil, errors.New("git-svnserver: a mixed-revision working copy is not supported")
		}
		rev, err := repo.ResolveRev(updateRev)
		if err != nil {
			return nil, err
		}
		// path is report[0].Path itself, unmodified: it's already in the
		// same session-anchor-relative form server.List and
		// server.GetFile's own reqPath parameter takes (CheckoutEdit and
		// UpdateEdit call back into those closures internally, which
		// resolve it against sessionBase themselves), and, for
		// UpdateEdit, path must always be a directory -- target (a
		// separate argument, possibly a multi-segment path reaching a
		// plain file) is how a single-file update/diff target is
		// described instead, without treating the file itself as if it
		// were the report's own root.
		path := report[0].Path
		if svn.IsPlainCheckout(report) {
			return server.CheckoutEdit(path, rev)
		}
		if fromRev, ok := svn.IsSingleRevisionUpdate(report); ok {
			return server.UpdateEdit(path, target, fromRev, rev)
		}
		return nil, errors.New("git-svnserver: only a plain checkout or a single-revision update of the whole working copy is supported")
	}

	return server.Serve(in, out)
}

// filterByPattern keeps only the entries whose base name matches at least
// one of the given glob patterns, or all of them if pattern is empty. The
// queried directory's own entry (dirents[0]; see gitrepo.Repo.List) is
// always kept regardless: a real svnserve always includes it in a "list"
// response (confirmed by testing against one), so this conservatively
// keeps that shape even under a pattern filter, though it wasn't tested
// specifically against a real svnserve's own "--search" handling.
func filterByPattern(dirents []svn.Dirent, pattern []string) []svn.Dirent {
	if len(pattern) == 0 || len(dirents) == 0 {
		return dirents
	}
	kept := dirents[:1]
	for _, d := range dirents[1:] {
		for _, p := range pattern {
			if ok, _ := path.Match(p, path.Base(d.Path)); ok {
				kept = append(kept, d)
				break
			}
		}
	}
	return kept
}

// withBase resolves an SVN path from the client against the session's base
// path (the part of the client's connect URL that fell below the git
// repository root; see gitrepo.FindRepo), turning it into a path relative
// to the repository root.
func withBase(base, path string) string {
	base = strings.Trim(base, "/")
	path = strings.Trim(path, "/")
	switch {
	case base == "":
		return path
	case path == "":
		return base
	default:
		return base + "/" + path
	}
}

// stripBase is withBase's inverse: given full (a repository-root-relative
// path) and base (the session's own base path), it returns full's own
// remainder below base. Used for a "diff" command's versusURL, the one
// path git-svnserver ever has in repository-root-relative form to begin
// with (see svn.RepoRelativePath) rather than session-relative, unlike
// every path a client sends directly.
func stripBase(base, full string) string {
	base = strings.Trim(base, "/")
	full = strings.Trim(full, "/")
	if base == "" {
		return full
	}
	return strings.TrimPrefix(strings.TrimPrefix(full, base), "/")
}
