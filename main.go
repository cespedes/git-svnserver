package main

import (
	"fmt"
	"log"
	"os"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: git-svnserver <repo.git>")
		os.Exit(1)
	}
	repo, err := git.PlainOpen(os.Args[1])

	if err != nil {
		log.Fatal(err)
	}

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
}
