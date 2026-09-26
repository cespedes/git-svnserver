package gitrepo

import (
	"errors"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// revMap is the persistent state SVN needs on top of a plain Git history:
// a mapping between SVN revision numbers and Git commits. It is stored as
// a plain text file, one commit hash per line, ordered oldest-first;
// revision N (1-based) is the commit on line N. Revision 0 is implicit
// (the empty tree, before the first commit) and is never stored.
type revMap struct {
	path string
	revs []plumbing.Hash
}

func loadRevMap(path string) (*revMap, error) {
	rm := &revMap{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return rm, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		rm.revs = append(rm.revs, plumbing.NewHash(line))
	}
	return rm, nil
}

// save writes the revision map back to disk, atomically (so a crash or a
// concurrent reader never observes a half-written file).
func (rm *revMap) save() error {
	var b strings.Builder
	for _, h := range rm.revs {
		b.WriteString(h.String())
		b.WriteByte('\n')
	}
	tmp := rm.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, rm.path)
}

// update extends rm so its last entry is tip, the branch's current commit,
// walking the branch's first-parent history to discover which commits are
// new since the map was last saved. If tip isn't a descendant of any
// commit already in the map (e.g. the branch was rebased or reset), the
// map is rebuilt from scratch by walking all the way back to the root
// commit. It is a no-op, and does not touch the file on disk, if the map
// is already up to date.
func (rm *revMap) update(repo *git.Repository, tip plumbing.Hash) error {
	if n := len(rm.revs); n > 0 && rm.revs[n-1] == tip {
		return nil
	}

	known := make(map[plumbing.Hash]int, len(rm.revs))
	for i, h := range rm.revs {
		known[h] = i
	}

	cur, err := repo.CommitObject(tip)
	if err != nil {
		return err
	}
	var newChain []plumbing.Hash
	matchIdx := -1
	for {
		if idx, ok := known[cur.Hash]; ok {
			matchIdx = idx
			break
		}
		newChain = append(newChain, cur.Hash)
		if cur.NumParents() == 0 {
			break
		}
		cur, err = cur.Parent(0)
		if err != nil {
			return err
		}
	}
	for i, j := 0, len(newChain)-1; i < j; i, j = i+1, j-1 {
		newChain[i], newChain[j] = newChain[j], newChain[i]
	}

	if matchIdx >= 0 {
		rm.revs = append(rm.revs[:matchIdx+1:matchIdx+1], newChain...)
	} else {
		rm.revs = newChain
	}
	return rm.save()
}
