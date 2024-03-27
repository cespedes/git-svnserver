package main

import (
	"bufio"
	"fmt"
	"log"
	"net/url"
	"os"
	"path"
	"slices"

	"github.com/cespedes/svn"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func main() {
	run()
}

type App struct {
	Server  svn.Server
	RepoDir string
	Repo    *git.Repository
}

func run() error {
	var app App

	app.Server.Greet = func(version int, capabilities []string, URL string,
		raclient string, client *string) (svn.ReposInfo, error) {
		var reposInfo svn.ReposInfo

		u, err := url.Parse(URL)
		if err != nil {
			return reposInfo, err
		}
		app.RepoDir = u.Path
		app.Repo, err = git.PlainOpen(app.RepoDir)
		if err != nil {
			return reposInfo, err
		}

		reposInfo.UUID = "c5a7a7b1-3e3e-4c98-a541-f46ece210564"
		reposInfo.URL = URL
		reposInfo.Capabilities = make([]string, 0)

		return reposInfo, nil
	}

	app.Server.GetLatestRev = func() (int, error) {
		svnRevs, err := syncSvnRevs(app.RepoDir, app.Repo)
		if err != nil {
			return 0, err
		}

		lastRev := len(svnRevs) - 1
		return lastRev, nil
	}
	app.Server.Stat = func(path string, rev *uint) (svn.Dirent, error) {
		svnRevs, err := syncSvnRevs(app.RepoDir, app.Repo)
		if err != nil {
			return svn.Dirent{}, err
		}

		lastRev := uint(len(svnRevs) - 1)

		return svn.Dirent{
			Kind:        "dir",
			CreatedRev:  lastRev,
			CreatedDate: "2024-03-18T14:50:07.758412Z",
		}, nil
	}
	app.Server.CheckPath = func(path string, rev *uint) (string, error) {
		return "dir", nil
	}

	err := app.Server.Serve(os.Stdin, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}

	return nil

	/*
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
		lastRev := len(svnRevs) - 1
		fmt.Printf("Last rev: %d\n", lastRev)
		fmt.Printf("rev[0] = %s\n", svnRevs[0])
		if lastRev > 0 {
			fmt.Printf("rev[1] = %s\n", svnRevs[1])
			fmt.Printf("rev[%d] = %s\n", lastRev, svnRevs[lastRev])
		}

		return nil
	*/
}

func syncSvnRevs(repodir string, repo *git.Repository) ([]plumbing.Hash, error) {
	if _, err := os.Lstat(path.Join(repodir, ".git")); err == nil {
		repodir = path.Join(repodir, ".git")
	}
	svnHashes := []plumbing.Hash{plumbing.ZeroHash}
	svnMap := make(map[plumbing.Hash]bool)
	revsFile := path.Join(repodir, "git-svn-refs.txt")
	fp, err := os.Open(revsFile)
	if err == nil {
		scanner := bufio.NewScanner(fp)
		for scanner.Scan() {
			h := plumbing.NewHash(scanner.Text())
			svnMap[h] = true
			svnHashes = append(svnHashes, h)
		}
		fp.Close()
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", revsFile, err)
		}
	}
	fp = nil

	var gitHashes []plumbing.Hash

	logs, err := repo.Log(&git.LogOptions{Order: git.LogOrderCommitterTime})
	if err != nil {
		log.Fatal(err)
	}
	err = logs.ForEach(func(c *object.Commit) error {
		gitHashes = append(gitHashes, c.Hash)
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	slices.Reverse(gitHashes)
	// fmt.Printf("len(gitHashes) = %d\n", len(gitHashes))
	for _, h := range gitHashes {
		if !svnMap[h] {
			// fmt.Printf("Hash %s not found in SVN map\n", h)
			if fp == nil {
				fp, err = os.OpenFile(revsFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
				if err != nil {
					return nil, err
				}
				defer fp.Close()
			}
			_, err = fp.WriteString(h.String() + "\n")
			if err != nil {
				return nil, err
			}
			svnMap[h] = true
			svnHashes = append(svnHashes, h)
		}
	}

	return svnHashes, nil
}

/*
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
