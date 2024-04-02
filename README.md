# git-svnserver

This project will be a Subversion server used to
access Git repositories, written in Go.

This is a work in progress, and it is in a very early stage:
right now, **many things do not work** (yet).

# How does this work

The program accepts a path to a Git repo.

The list of all the commits up to the HEAD are stored in `.git/git-svn-refs.txt`,
which is used to number them as SVN revisions.

Function `syncSvnRevs()` reads all the commits from `.git/git-svn-refs.txt` and,
if any commit is missing, it adds them to that file.

**WARNING**: There are no locks (yet) when reading and writing file `.git/git-svn-refs.txt`.

# Basic functionality

The following SVN protocol commands are currently implemented:

- `get-latest-rev`
- `stat`
- `list`
- `get-file`

The following SVN commands are currently working against this server (at least in part):

- `svn info`
- `svn ls`
- `svn cat`
- `svn log`

# TO-DO list

- "last changed" (author, rev and date) show information for
  the whole commit, not for the file inside the commit.
- Command `update` (needed for `svn checkout`)

# Non-working SVN commands

- `svn checkout`
- `svn blame`
- `svn proplist`
