// Package gitrepo adapts a Git repository (its main branch only, for now)
// so it can be served as if it were a Subversion repository: each commit
// on that branch becomes one SVN revision, oldest commit first.
package gitrepo

import (
	"crypto/md5"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// Repo is a Git repository exposed as a (read-only, single-branch) SVN
// repository.
type Repo struct {
	git    *git.Repository
	fsPath string // the repository's working/bare directory, as opened
	gitDir string // fsPath itself (bare repo) or fsPath/.git (non-bare)
	revmap *revMap
}

// FindRepo locates the Git repository that a client-supplied path refers
// to: it walks up from path looking for the first ancestor directory that
// is a valid Git repository, the same way a real svnserve (when it isn't
// jailed with "-r") walks up looking for the first ancestor that is a
// valid SVN repository. The returned subPath is whatever remainder of path
// lies below that repository root (e.g. "trunk/sub"), which the caller
// should use as a prefix for every subsequent request in the session.
func FindRepo(path string) (repo *Repo, subPath string, err error) {
	path = filepath.Clean(path)
	cur := path
	var suffix []string
	for {
		// Any error here (not just git.ErrRepositoryNotExists) means cur
		// isn't a repository root: e.g. it may not exist at all, or (for
		// any but the first, client-supplied cur) be a regular file one
		// of whose ancestors is the actual repository root. Either way,
		// the right move is the same: keep walking up.
		if gr, gerr := git.PlainOpen(cur); gerr == nil {
			r, rerr := newRepo(cur, gr)
			if rerr != nil {
				return nil, "", rerr
			}
			return r, filepath.Join(suffix...), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil, "", fmt.Errorf("gitrepo: no git repository found in %q or any of its parent directories", path)
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		cur = parent
	}
}

func newRepo(fsPath string, gr *git.Repository) (*Repo, error) {
	gitDir := fsPath
	if fi, err := os.Stat(filepath.Join(fsPath, ".git")); err == nil && fi.IsDir() {
		gitDir = filepath.Join(fsPath, ".git")
	}
	rm, err := loadRevMap(filepath.Join(gitDir, "svnserver-revmap"))
	if err != nil {
		return nil, fmt.Errorf("gitrepo: loading revision map: %w", err)
	}
	r := &Repo{git: gr, fsPath: fsPath, gitDir: gitDir, revmap: rm}
	if err := r.syncRevMap(); err != nil {
		return nil, fmt.Errorf("gitrepo: updating revision map: %w", err)
	}
	return r, nil
}

// syncRevMap makes the on-disk revision map match the branch's current
// history, extending it (or, if the branch's history was rewritten,
// rebuilding it from scratch) as needed. See revmap.go.
func (r *Repo) syncRevMap() error {
	head, err := r.git.Head()
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			// An unborn HEAD: the repository has no commits yet, so
			// there is nothing to map (revision 0 is all there is).
			return nil
		}
		return err
	}
	return r.revmap.update(r.git, head.Hash())
}

// UUID returns a stable identifier for the repository, derived from its
// location on disk, so it stays the same across server restarts without
// needing to store it anywhere separately.
func (r *Repo) UUID() string {
	sum := md5.Sum([]byte(r.gitDir))
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// LatestRev returns the repository's latest SVN revision number, i.e. the
// number of commits on the tracked branch.
func (r *Repo) LatestRev() uint {
	return uint(len(r.revmap.revs))
}

// ResolveRev returns rev, or the latest revision if rev is nil, checking
// that it is a valid revision number for this repository either way.
func (r *Repo) ResolveRev(rev *uint) (uint, error) {
	latest := r.LatestRev()
	if rev == nil {
		return latest, nil
	}
	if *rev > latest {
		return 0, fmt.Errorf("gitrepo: no such revision %d (latest is %d)", *rev, latest)
	}
	return *rev, nil
}
