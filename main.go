package main

import (
	"bufio"
	"crypto/md5"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/cespedes/svn"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const logFile = ""

func main() {
	run()
}

type App struct {
	Server   svn.Server
	Target   string // target URL received from the client
	RepoDir  string // absolute PATH to the repository
	Relative string // relative PATH inside the repository
	Repo     *git.Repository
	SvnRevs  []plumbing.Hash
	Log      io.Writer
}

func run() error {
	var app App

	if logFile != "" {
		app.Log, _ = os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	}

	app.Server.Greet = func(version int, capabilities []string, URL string,
		raclient string, client *string) (svn.ReposInfo, error) {
		var reposInfo svn.ReposInfo

		if app.Log != nil {
			fmt.Fprintf(app.Log, "Greet(%d,%v,%q,%q,%v)\n",
				version, capabilities, URL, raclient, client)
		}

		u, err := url.Parse(URL)
		if err != nil {
			return reposInfo, err
		}

		app.Target = URL
		targetPath := path.Clean(u.Path)
		u.Path = targetPath
		for {
			repo, err := git.PlainOpen(u.Path)
			if err == nil {
				app.RepoDir = u.Path
				app.Relative = strings.TrimPrefix(targetPath, app.RepoDir)
				app.Relative = strings.TrimPrefix(app.Relative, "/")
				app.Repo = repo
				break
			}
			u.Path = path.Join(u.Path, "..")
			if u.Path == "/" {
				return reposInfo, err
			}
		}

		if app.Log != nil {
			fmt.Fprintf(app.Log, "app=%+v\n", app)
		}

		reposInfo.URL = u.String()
		uuid := md5.Sum([]byte(app.RepoDir))
		reposInfo.UUID = fmt.Sprintf("%x%x%x%x-%x%x-%x%x-%x%x-%x%x%x%x%x%x",
			uuid[0], uuid[1], uuid[2], uuid[3],
			uuid[4], uuid[5], uuid[6], uuid[7], uuid[8], uuid[9],
			uuid[10], uuid[11], uuid[12], uuid[13], uuid[14], uuid[15])
		reposInfo.Capabilities = make([]string, 0)

		app.SvnRevs, err = syncSvnRevs(app.RepoDir, app.Repo)
		if err != nil {
			return reposInfo, err
		}

		return reposInfo, nil
	}

	app.Server.GetLatestRev = func() (int, error) {
		if app.Log != nil {
			fmt.Fprintf(app.Log, "GetLastRev()\n")
		}
		lastRev := len(app.SvnRevs) - 1
		return lastRev, nil
	}
	app.Server.Stat = func(p string, prev *uint) (svn.Dirent, error) {
		if app.Log != nil {
			fmt.Fprintf(app.Log, "Stat(%q->%q,%v)\n", p, path.Join(app.Relative, p), prev)
		}
		var rev uint
		if prev != nil {
			rev = *prev
		} else {
			rev = uint(len(app.SvnRevs) - 1)
		}
		if rev >= uint(len(app.SvnRevs)) {
			return svn.Dirent{}, fmt.Errorf("no such revision %d", rev)
		}

		commit, err := app.Repo.CommitObject(app.SvnRevs[rev])
		if err != nil {
			return svn.Dirent{}, err
		}
		kind := "dir"
		var size uint64
		if app.Relative != "" || p != "" {
			p = path.Join(app.Relative, p)
			tree, err := commit.Tree()
			if err != nil {
				return svn.Dirent{}, err
			}
			entry, err := tree.FindEntry(p)
			if err != nil {
				return svn.Dirent{}, err
			}
			switch entry.Mode {
			case filemode.Dir:
			case filemode.Regular, filemode.Deprecated, filemode.Executable, filemode.Symlink:
				// TODO maybe filemode.Symlink should return something else?
				kind = "file"
				file, err := tree.TreeEntryFile(entry)
				if err != nil {
					return svn.Dirent{}, err
				}
				size = uint64(file.Size)
			default:
				return svn.Dirent{}, fmt.Errorf("%s: filemode %o unsupported", p, entry.Mode)
			}
		}

		return svn.Dirent{
			Kind:        kind,
			Size:        size,
			CreatedRev:  rev,
			LastAuthor:  commit.Author.Email,
			CreatedDate: commit.Author.When.UTC().Format("2006-01-02T15:04:05.000000Z"),
		}, nil
	}
	app.Server.List = func(p string, prev *uint, depth string, fields []string, pattern []string) ([]svn.Dirent, error) {
		var rev uint
		if prev != nil {
			rev = *prev
		} else {
			rev = uint(len(app.SvnRevs) - 1)
		}

		commit, err := app.Repo.CommitObject(app.SvnRevs[rev])
		if err != nil {
			return nil, err
		}
		tree, err := commit.Tree()
		if err != nil {
			return nil, err
		}

		pp := path.Join(app.Relative, p)
		if pp != "" {
			tree, err = tree.Tree(pp)
			if err != nil {
				dir, err := app.Server.Stat(p, prev)
				if err != nil {
					return nil, err
				}
				dir.Path = path.Join("/", app.Relative, p)
				return []svn.Dirent{dir}, nil
			}
		}

		dirents := make([]svn.Dirent, 0)
		for _, entry := range tree.Entries {
			kind := "dir"
			var size uint64
			switch entry.Mode {
			case filemode.Dir:
			case filemode.Regular, filemode.Deprecated, filemode.Executable, filemode.Symlink:
				// TODO maybe filemode.Symlink should return something else?
				kind = "file"
				file, err := tree.TreeEntryFile(&entry)
				if err != nil {
					return nil, err
				}
				size = uint64(file.Size)
			default:
				return nil, fmt.Errorf("%s: filemode %o unsupported", app.Relative, entry.Mode)
			}
			dirents = append(dirents, svn.Dirent{
				Path:        path.Join("/", pp, entry.Name),
				Kind:        kind,
				Size:        size,
				HasProps:    false,
				CreatedRev:  rev,
				LastAuthor:  commit.Author.Email,
				CreatedDate: commit.Author.When.UTC().Format("2006-01-02T15:04:05.000000Z"),
			})
		}
		return dirents, nil
	}
	app.Server.GetFile = func(p string, prev *uint, wantProps bool, wantContents bool) (uint, []svn.PropList, []byte, error) {
		var rev uint
		if prev != nil {
			rev = *prev
		} else {
			rev = uint(len(app.SvnRevs) - 1)
		}
		if rev >= uint(len(app.SvnRevs)) {
			return 0, nil, nil, fmt.Errorf("no such revision %d", rev)
		}
		commit, err := app.Repo.CommitObject(app.SvnRevs[rev])
		if err != nil {
			return 0, nil, nil, err
		}
		if app.Relative != "" || p != "" {
			p = path.Join(app.Relative, p)
			tree, err := commit.Tree()
			if err != nil {
				return 0, nil, nil, err
			}
			file, err := tree.File(p)
			if err != nil {
				return 0, nil, nil, err
			}
			rd, err := file.Blob.Reader()
			if err != nil {
				return 0, nil, nil, err
			}
			defer rd.Close()
			content, err := io.ReadAll(rd)
			if err != nil {
				return 0, nil, nil, err
			}
			return rev, nil, content, nil
		}
		return 0, nil, nil, fmt.Errorf("Attempted to get checksum of a *non*-file node")
	}
	//app.Server.CheckPath = func(path string, rev *uint) (string, error) {
	//	return "dir", nil
	//}

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
