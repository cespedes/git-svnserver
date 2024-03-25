# git-svnserver

This project will be a Subversion server used to
access Git repositories, written in Go.

This is a work in progress, and it is in a very early stage:
right now, it does **not** work (at all).

## Current functionality

The program accepts a path to a Git repo.

The list of all the commits up to the HEAD are stored in `.git/git-svn-refs.txt`,
which is used to number them as SVN revisions.

Function `svnSvnRevs()` reads all the commits from `.git/git-svn-refs.txt` and,
if any commit is missing, it adds them to that file.

**WARNING**: There are no locks (yet) when reading and writing file `.git/git-svn-refs.txt`.
