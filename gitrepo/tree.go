package gitrepo

import (
	"fmt"
	"io"
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
	// identity distinguishes two revisions of the same path: the blob
	// hash for a file, or a signature of the directory's own direct
	// entries (not a recursive tree hash) for a directory -- see
	// dirIdentity.
	identity string
	size     uint64
}

// dirIdentity returns a string that changes if and only if t's own direct
// entries (their names and kinds) do, deliberately ignoring the recursive
// content of any subdirectories: this is what makes lastChangedRev match
// SVN's semantics for a directory, where its own last-changed-rev only
// bumps when something is added, removed or replaced directly inside it,
// not when a file nested deeper down merely changes content. (A Git
// tree's own hash, in contrast, is a Merkle hash: it changes on every
// change anywhere in the subtree, which would make every ancestor
// directory look "changed" on every single commit.)
func dirIdentity(t *object.Tree) string {
	var b strings.Builder
	for _, e := range t.Entries {
		b.WriteString(e.Name)
		b.WriteByte(0)
		b.WriteString(kindFromMode(e.Mode))
		b.WriteByte(0)
	}
	return b.String()
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
		return treeEntry{mode: filemode.Dir, identity: dirIdentity(tree)}, true, nil
	}
	te, err := tree.FindEntry(path)
	if err != nil {
		return treeEntry{}, false, nil
	}
	e := treeEntry{mode: te.Mode}
	if te.Mode == filemode.Dir {
		subtree, err := tree.Tree(path)
		if err != nil {
			return treeEntry{}, false, err
		}
		e.identity = dirIdentity(subtree)
	} else {
		e.identity = te.Hash.String()
		if te.Mode.IsFile() {
			if sz, err := tree.Size(path); err == nil {
				e.size = uint64(sz)
			}
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
		if !found || prev.identity != cur.identity || prev.mode != cur.mode {
			break
		}
		rev--
	}
	return rev, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000Z")
}

// direntAt builds the svn.Dirent for path (which must exist at atRev, with
// the given entry), filling in CreatedRev/CreatedDate/LastAuthor from
// whichever earlier revision last changed it.
func (r *Repo) direntAt(path string, atRev uint, e treeEntry) (svn.Dirent, error) {
	createdRev, err := r.lastChangedRev(path, atRev)
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
	return r.direntAt(path, resolved, e)
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

// wirePath turns path (repo-root-relative, no leading slash; "" for the
// root) into the full, slash-prefixed form svn.Server expects for every
// Dirent.Path (in a "list" response) and ChangedPath.Path (in a "log"
// response with changed paths; see log.go).
func wirePath(path string) string {
	return "/" + path
}

// List answers an SVN "list" command: path itself (which must be a
// directory), plus its direct children, at rev (or at the latest
// revision, if rev is nil) -- matching a real svnserve, which always
// includes the queried directory among the entries it returns.
//
// Only a directory's immediate children are ever returned, regardless of
// what depth the client asked for: recursing arbitrarily deep is left for
// later.
func (r *Repo) List(path string, rev *uint) ([]svn.Dirent, error) {
	path = normalizePath(path)
	resolved, err := r.resolveRev(rev)
	if err != nil {
		return nil, err
	}
	e, found, err := r.entryAt(resolved, path)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("gitrepo: %q: %w", path, fs.ErrNotExist)
	}
	if e.mode != filemode.Dir {
		return nil, fmt.Errorf("gitrepo: %q: not a directory", path)
	}

	tree, err := r.treeForRev(resolved)
	if err != nil {
		return nil, err
	}
	dir := tree
	if path != "" {
		dir, err = tree.Tree(path)
		if err != nil {
			return nil, err
		}
	}

	self, err := r.direntAt(path, resolved, e)
	if err != nil {
		return nil, err
	}
	self.Path = wirePath(path)
	dirents := make([]svn.Dirent, 0, 1+len(dir.Entries))
	dirents = append(dirents, self)

	for _, te := range dir.Entries {
		childPath := te.Name
		if path != "" {
			childPath = path + "/" + te.Name
		}
		// Reuse entryAt rather than building a treeEntry from te
		// directly, so a child directory gets the same dirIdentity
		// treatment (direct entries only, not a recursive hash) as
		// every other directory lookup.
		childEntry, found, err := r.entryAt(resolved, childPath)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("gitrepo: %q: %w", childPath, fs.ErrNotExist)
		}
		d, err := r.direntAt(childPath, resolved, childEntry)
		if err != nil {
			return nil, err
		}
		d.Path = wirePath(childPath)
		dirents = append(dirents, d)
	}
	return dirents, nil
}

// GetFile answers an SVN "get-file" command: the revision path was last
// changed at (at or before rev, or the latest revision if rev is nil), and
// its content if wantContents is true. It returns an error satisfying
// errors.Is(err, fs.ErrNotExist) if path does not exist at that revision.
func (r *Repo) GetFile(path string, rev *uint, wantContents bool) (uint, []byte, error) {
	path = normalizePath(path)
	resolved, err := r.resolveRev(rev)
	if err != nil {
		return 0, nil, err
	}
	e, found, err := r.entryAt(resolved, path)
	if err != nil {
		return 0, nil, err
	}
	if !found {
		return 0, nil, fmt.Errorf("gitrepo: %q: %w", path, fs.ErrNotExist)
	}
	if e.mode == filemode.Dir {
		return 0, nil, fmt.Errorf("gitrepo: %q: is a directory", path)
	}
	createdRev, err := r.lastChangedRev(path, resolved)
	if err != nil {
		return 0, nil, err
	}
	if !wantContents {
		return createdRev, nil, nil
	}

	tree, err := r.treeForRev(resolved)
	if err != nil {
		return 0, nil, err
	}
	file, err := tree.File(path)
	if err != nil {
		return 0, nil, err
	}
	blob, err := file.Reader()
	if err != nil {
		return 0, nil, err
	}
	defer blob.Close()
	content, err := io.ReadAll(blob)
	if err != nil {
		return 0, nil, err
	}
	return createdRev, content, nil
}
