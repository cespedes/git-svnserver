package gitrepo

import (
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/cespedes/svn"
)

// normalizePath strips the leading/trailing slashes an SVN path may carry,
// since Git tree lookups want a plain "a/b/c" relative path ("" for the
// repository root).
func normalizePath(path string) string {
	return strings.Trim(path, "/")
}

func kindFromMode(m filemode.FileMode) string {
	if m == filemode.Dir {
		return "dir"
	}
	// Regular, Executable, Symlink and Submodule entries are all served
	// as plain files for now: SVN has no equivalent of a submodule, and
	// symlinks would need the svn:special property to round-trip
	// correctly, which isn't implemented yet.
	return "file"
}

// commitForRev returns the commit for a given revision. rev must be a
// valid, non-zero revision number (see resolveRev).
func (r *Repo) commitForRev(rev uint) (*object.Commit, error) {
	if rev == 0 || rev > r.LatestRev() {
		return nil, fmt.Errorf("gitrepo: no such revision %d", rev)
	}
	return r.git.CommitObject(r.revmap.revs[rev-1])
}

// treeForRev returns the tree at rev, or nil (meaning the empty tree) for
// revision 0.
func (r *Repo) treeForRev(rev uint) (*object.Tree, error) {
	if rev == 0 {
		return nil, nil
	}
	c, err := r.commitForRev(rev)
	if err != nil {
		return nil, err
	}
	return c.Tree()
}

// treeEntry is what entryAt reports about a path: enough to tell whether
// two revisions of it are the same, and to fill in an svn.Dirent/Stat.
type treeEntry struct {
	mode filemode.FileMode
	hash string // hex object hash; empty for the (synthetic) root entry
	size uint64
}

// entryAt looks up path in the tree at rev, reporting whether it exists.
func (r *Repo) entryAt(rev uint, path string) (treeEntry, bool, error) {
	tree, err := r.treeForRev(rev)
	if err != nil {
		return treeEntry{}, false, err
	}
	if tree == nil {
		return treeEntry{}, false, nil
	}
	if path == "" {
		return treeEntry{mode: filemode.Dir, hash: tree.Hash.String()}, true, nil
	}
	te, err := tree.FindEntry(path)
	if err != nil {
		return treeEntry{}, false, nil
	}
	e := treeEntry{mode: te.Mode, hash: te.Hash.String()}
	if te.Mode.IsFile() {
		if sz, err := tree.Size(path); err == nil {
			e.size = uint64(sz)
		}
	}
	return e, true, nil
}

// lastChangedRev returns the most recent revision, at or before atRev, in
// which path's content or mode changed -- the "created-rev" SVN reports
// for a Stat/Dirent. path must exist at atRev.
func (r *Repo) lastChangedRev(path string, atRev uint) (uint, error) {
	cur, found, err := r.entryAt(atRev, path)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("gitrepo: %q: %w", path, fs.ErrNotExist)
	}
	rev := atRev
	for rev > 1 {
		prev, found, err := r.entryAt(rev-1, path)
		if err != nil {
			return 0, err
		}
		if !found || prev.hash != cur.hash || prev.mode != cur.mode {
			break
		}
		rev--
	}
	return rev, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000Z")
}

// Stat answers an SVN "stat" command: the status of path at rev (or at the
// latest revision, if rev is nil). It returns an error satisfying
// errors.Is(err, fs.ErrNotExist) if path does not exist at that revision.
func (r *Repo) Stat(path string, rev *uint) (svn.Dirent, error) {
	path = normalizePath(path)
	resolved, err := r.resolveRev(rev)
	if err != nil {
		return svn.Dirent{}, err
	}
	e, found, err := r.entryAt(resolved, path)
	if err != nil {
		return svn.Dirent{}, err
	}
	if !found {
		return svn.Dirent{}, fmt.Errorf("gitrepo: %q: %w", path, fs.ErrNotExist)
	}
	createdRev, err := r.lastChangedRev(path, resolved)
	if err != nil {
		return svn.Dirent{}, err
	}
	commit, err := r.commitForRev(createdRev)
	if err != nil {
		return svn.Dirent{}, err
	}
	return svn.Dirent{
		Kind:        kindFromMode(e.mode),
		Size:        e.size,
		HasProps:    false,
		CreatedRev:  createdRev,
		CreatedDate: formatTime(commit.Author.When),
		LastAuthor:  commit.Author.Name,
	}, nil
}

// CheckPath answers an SVN "check-path" command: the node kind of path at
// rev (or at the latest revision, if rev is nil), or "none" if it does not
// exist there.
func (r *Repo) CheckPath(path string, rev *uint) (string, error) {
	path = normalizePath(path)
	resolved, err := r.resolveRev(rev)
	if err != nil {
		return "", err
	}
	e, found, err := r.entryAt(resolved, path)
	if err != nil {
		return "", err
	}
	if !found {
		return "none", nil
	}
	return kindFromMode(e.mode), nil
}
