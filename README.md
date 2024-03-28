# git-svnserver

This project will be a Subversion server used to
access Git repositories, written in Go.

This is a work in progress, and it is in a very early stage:
right now, it does **not** work (at all).

# Basic functionality

In order to do "svn info" or "svn ls -v" we need, for each revision,
a list of all the files and directories in that revision, and,
for each of them:
- File name
- File type
- File size
- Last Changed Author (e-mail address)
- Last Changed Rev
- Last Changed Date

At least the latest revision is expected to run quickly.

## Current functionality

The program accepts a path to a Git repo.

The list of all the commits up to the HEAD are stored in `.git/git-svn-refs.txt`,
which is used to number them as SVN revisions.

Function `syncSvnRevs()` reads all the commits from `.git/git-svn-refs.txt` and,
if any commit is missing, it adds them to that file.

**WARNING**: There are no locks (yet) when reading and writing file `.git/git-svn-refs.txt`.
