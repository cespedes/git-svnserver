# git-svnserver

`git-svnserver` behaves like `svnserve -t`: it speaks the [SVN protocol
(ra_svn)](https://svn.apache.org/repos/asf/subversion/trunk/subversion/libsvn_ra_svn/protocol)
over its standard input/output, the way a real `svnserve` does when invoked
over `ssh` for a `svn+ssh://` URL. Instead of a real Subversion repository,
though, it serves the content of a **Git repository's main branch**, mapping
each of its commits to one SVN revision — so existing SVN clients and
tooling can browse a Git repository's history as if it were an SVN one.

It is built on top of [`github.com/cespedes/svn`](https://github.com/cespedes/svn)
(the ra_svn protocol implementation) and
[`github.com/go-git/go-git`](https://github.com/go-git/go-git) (reading the
Git repository itself).

This is a work in progress. Only one branch — the repository's `HEAD` — is
exposed, there is no write support (SVN clients see a read-only repository),
and only a subset of ra_svn is implemented so far.

## Status

| `svn` subcommand | Works? |
| --- | --- |
| `info` | ✅ |
| `ls` / `cat` / `log` | ❌ (not implemented yet) |
| `checkout` / `update` / `switch` | ❌ (not implemented yet) |
| `commit` and any other write operation | ❌ (not planned: this is a read-only view of a Git repository) |

## How revisions map to commits

SVN revisions are consecutive integers; Git history (even on a single
branch) is a DAG. To bridge the two, each commit reached by following a
branch's **first-parent** history from its tip is numbered in order, oldest
first: revision 1 is the first commit, revision 2 the next one on that
chain, and so on. Revision 0 is the (implicit) empty repository, before the
first commit.

This mapping is cached in a file named `svnserver-revmap`, inside the
repository's `.git` directory: it is created the first time the repository
is served, and extended (or, if the branch's history was rewritten, e.g. by
a rebase, fully rebuilt) every time it is served again and the branch has
moved.

## Locating the repository

Like a real `svnserve -t` invoked without `-r` (no jailed root), the
repository to serve isn't given on the command line: it comes from the
`path` component of the URL the connecting client sends during the
protocol handshake (e.g. the `/home/user/repo` in
`svn+ssh://host/home/user/repo`). `git-svnserver` walks up from that path
looking for the first ancestor directory that is a valid Git repository,
the same way a real `svnserve` looks for the first ancestor that is a
valid SVN repository; anything below that root (e.g. `trunk/sub` in
`svn+ssh://host/home/user/repo/trunk/sub`) is treated as a path inside the
repository, not as part of locating it.

## Usage

`git-svnserver` is meant to be invoked as an SSH tunnel command, exactly
like `svnserve -t`:

```sh
ssh user@host git-svnserver -t
```

To try it out locally without SSH, `svn`'s own
[tunnel mechanism](https://svnbook.red-bean.com/en/1.7/svn.serverconfig.svnserve.html#svn.serverconfig.svnserve.sshtunnel)
can point straight at the built binary:

```sh
go build -o git-svnserver .

svn info \
  --config-option=config:tunnels:gitsvnserver=$PWD/git-svnserver \
  svn+gitsvnserver://localhost/absolute/path/to/some/git/repo
```

## Development

```sh
go build ./...
go test ./...
```

If `git` and `svn` are installed, `go test ./...` also runs integration
tests that create a temporary Git repository and run a real `svn` client
against it (through the tunnel mechanism above, without any network
involved); they're skipped cleanly otherwise.

## License

[MIT](LICENSE)
