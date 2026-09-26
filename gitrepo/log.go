package gitrepo

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/utils/merkletrie"

	"github.com/cespedes/svn"
)

// rawHashAt returns the raw Git object hash at path in the tree at rev
// (the blob hash for a file, the recursive tree hash for a directory), or
// found=false if path does not exist there. Unlike entryAt's identity
// (used for Stat/List's last-changed-rev), this is sensitive to a change
// anywhere in a directory's subtree -- which is exactly what deciding
// whether a revision affects a path, for Log, needs.
func (r *Repo) rawHashAt(rev uint, path string) (hash string, found bool, err error) {
	tree, err := r.treeForRev(rev)
	if err != nil {
		return "", false, err
	}
	if tree == nil {
		return "", false, nil
	}
	if path == "" {
		return tree.Hash.String(), true, nil
	}
	te, err := tree.FindEntry(path)
	if err != nil {
		return "", false, nil
	}
	return te.Hash.String(), true, nil
}

// revAffectsPath reports whether rev changed path or anything under it
// (added, removed, or modified), compared to rev-1.
func (r *Repo) revAffectsPath(rev uint, path string) (bool, error) {
	cur, found, err := r.rawHashAt(rev, path)
	if err != nil {
		return false, err
	}
	prev, prevFound, err := r.rawHashAt(rev-1, path)
	if err != nil {
		return false, err
	}
	if found != prevFound {
		return true, nil
	}
	return found && cur != prev, nil
}

// existsAt reports whether path exists in tree (which may be nil, meaning
// the empty tree before the first commit).
func existsAt(tree *object.Tree, path string) bool {
	if tree == nil {
		return false
	}
	if path == "" {
		return true
	}
	_, err := tree.FindEntry(path)
	return err == nil
}

// ancestorDirs returns leafPath's ancestor directories, shallowest first
// (e.g. "trunk/sub/nested.txt" -> ["trunk", "trunk/sub"]).
func ancestorDirs(leafPath string) []string {
	var dirs []string
	for i := 0; i < len(leafPath); i++ {
		if leafPath[i] == '/' {
			dirs = append(dirs, leafPath[:i])
		}
	}
	return dirs
}

// changedPaths returns the paths rev added, removed or modified, compared
// to rev-1: one entry per changed file (from a Git tree diff, so this
// does not detect renames/copies -- a rename shows up as a delete plus an
// add), plus one entry for each ancestor directory that itself started or
// stopped existing as a direct result (e.g. adding the first file under a
// new directory also reports that directory as added) -- matching how a
// real svnserve reports directory add/remove alongside file changes.
func (r *Repo) changedPaths(rev uint) ([]svn.ChangedPath, error) {
	curTree, err := r.treeForRev(rev)
	if err != nil {
		return nil, err
	}
	var parentTree *object.Tree
	if rev > 1 {
		if parentTree, err = r.treeForRev(rev - 1); err != nil {
			return nil, err
		}
	}
	changes, err := object.DiffTree(parentTree, curTree)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var out []svn.ChangedPath
	addEntry := func(path, mode, kind string, textMods bool) {
		if seen[path] {
			return
		}
		seen[path] = true
		out = append(out, svn.ChangedPath{
			Path: wirePath(path),
			Mode: mode,
			Info: &svn.ChangedPathInfo{NodeKind: kind, TextMods: textMods},
		})
	}

	for _, change := range changes {
		action, err := change.Action()
		if err != nil {
			return nil, err
		}
		var leafPath, mode string
		var fileMode filemode.FileMode
		switch action {
		case merkletrie.Insert:
			leafPath, mode, fileMode = change.To.Name, "A", change.To.TreeEntry.Mode
		case merkletrie.Delete:
			leafPath, mode, fileMode = change.From.Name, "D", change.From.TreeEntry.Mode
		case merkletrie.Modify:
			leafPath, mode, fileMode = change.To.Name, "M", change.To.TreeEntry.Mode
		}
		addEntry(leafPath, mode, kindFromMode(fileMode), mode != "D")

		for _, dir := range ancestorDirs(leafPath) {
			curExists, parentExists := existsAt(curTree, dir), existsAt(parentTree, dir)
			switch {
			case curExists && !parentExists:
				addEntry(dir, "A", "dir", false)
			case !curExists && parentExists:
				addEntry(dir, "D", "dir", false)
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Log answers an SVN "log" command: the log entries for paths, between
// startRev and endRev inclusive. As a real "svn" client always does,
// startRev==0 is treated as "the latest revision", not literally
// revision 0; the returned order is descending (newest first) if
// startRev >= endRev, ascending otherwise -- matching a real svnserve.
//
// If changedPaths is true, each entry's Changed is filled in by
// changedPaths; copies/renames are not detected (see its doc comment).
func (r *Repo) Log(paths []string, startRev, endRev uint, changedPaths bool) ([]svn.LogEntry, error) {
	latest := r.LatestRev()
	if startRev == 0 {
		startRev = latest
	}
	if startRev > latest {
		return nil, fmt.Errorf("gitrepo: no such revision %d", startRev)
	}
	if endRev > latest {
		return nil, fmt.Errorf("gitrepo: no such revision %d", endRev)
	}

	descending := startRev >= endRev
	lo, hi := startRev, endRev
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo < 1 {
		lo = 1
	}

	var filterPaths []string
	for _, p := range paths {
		if p = normalizePath(p); p != "" {
			filterPaths = append(filterPaths, p)
		}
	}

	var revs []uint
	for rev := lo; rev <= hi; rev++ {
		if len(filterPaths) > 0 {
			affected := false
			for _, p := range filterPaths {
				ok, err := r.revAffectsPath(rev, p)
				if err != nil {
					return nil, err
				}
				if ok {
					affected = true
					break
				}
			}
			if !affected {
				continue
			}
		}
		revs = append(revs, rev)
	}
	if descending {
		for i, j := 0, len(revs)-1; i < j; i, j = i+1, j-1 {
			revs[i], revs[j] = revs[j], revs[i]
		}
	}

	entries := make([]svn.LogEntry, 0, len(revs))
	for _, rev := range revs {
		commit, err := r.commitForRev(rev)
		if err != nil {
			return nil, err
		}
		entry := svn.LogEntry{
			Rev:     rev,
			Author:  commit.Author.Name,
			Date:    formatTime(commit.Author.When),
			Message: strings.TrimSuffix(commit.Message, "\n"),
		}
		if changedPaths {
			if entry.Changed, err = r.changedPaths(rev); err != nil {
				return nil, err
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
