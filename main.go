package main

import (
	"fmt"
	"log"
	"os"
	"path"
	"slices"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func main() {
	run()
}

func run() error {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: git-svnserver <repo.git>")
		os.Exit(1)
	}
	repodir := os.Args[1]

	repo, err := git.PlainOpen(repodir)
	if err != nil {
		log.Fatal(err)
	}

	svnRevs, err := syncSvnRevs(repodir, repo)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("svnRevs: %v\n", svnRevs)

	/*
		branches, err := repo.Branches()
		if err != nil {
			log.Fatal(err)
		}
		err = branches.ForEach(func(ref *plumbing.Reference) error {
			fmt.Printf("one branch: %v\n", ref)
			//if ref.Type() == plumbing.HashReference {
			//	fmt.Println(ref)
			//}
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}

		head, err := repo.Head()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("head: %v\n", head)

		logs, err := repo.Log(&git.LogOptions{Order: git.LogOrderCommitterTime})
		if err != nil {
			log.Fatal(err)
		}
		err = logs.ForEach(func(c *object.Commit) error {
			fmt.Printf("history hash: %v\n", c.Hash)
			return nil
		})
		if err != nil {
			log.Fatal(err)
		}

		fs := osfs.New("/tmp")

		f, err := fs.OpenFile("git-svn-refs.txt", os.O_RDWR|os.O_CREATE, 0666)
		if err != nil {
			log.Fatal("openfile: " + err.Error())
		}
		defer f.Close()

		err = f.Lock()
		if err != nil {
			log.Fatal("lock: " + err.Error())
		}
		defer f.Unlock()
		fmt.Fprintln(f, "one line")
	*/
	return nil
}

func syncSvnRevs(repodir string, repo *git.Repository) ([]plumbing.Hash, error) {
	if _, err := os.Lstat(path.Join(repodir, ".git")); err == nil {
		repodir = path.Join(repodir, ".git")
	}
	revsFile := path.Join(repodir, "git-svn-refs.txt")
	_ = revsFile

	var hashes []plumbing.Hash

	logs, err := repo.Log(&git.LogOptions{Order: git.LogOrderCommitterTime})
	if err != nil {
		log.Fatal(err)
	}
	err = logs.ForEach(func(c *object.Commit) error {
		hashes = append(hashes, c.Hash)
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("len(hashes) = %d\n", len(hashes))
	hashes = append(hashes, plumbing.ZeroHash)
	slices.Reverse(hashes)
	return hashes, nil
}
