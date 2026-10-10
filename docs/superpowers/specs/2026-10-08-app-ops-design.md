# Running an app from the terminal: ps, cp, jobs, tasks, secrets, config files, projects

The commands an app needs between deployments, each on the endpoints the
dashboard uses, so the two never disagree. It extends
[the CLI's design](2026-10-08-cli-design.md); nothing changes on the server.

| Command | API |
|---|---|
| `ps [APP] [--state S]` | `GET .../apps/{appID}/service-tasks` |
| `cp SRC DST [--replica N]` | `POST .../container/file-upload`, `GET .../container/file-download` |
| `job ls`, `job run JOB`, `job enable\|disable JOB` | `.../sched-jobs`, `.../sched-jobs/{id}/exec`, `.../sched-jobs/{id}/status` |
| `task ls\|get\|cancel`, `logs --task ID` | `.../tasks`, `.../tasks/{id}`, `.../tasks/{id}/cancel`, `.../tasks/{id}/logs` (websocket) |
| `secret ls\|set\|rm` | `.../secrets` |
| `config-file ls\|push\|pull\|rm` | `.../config-files` |
| `project create\|delete`, `project env add\|rm` | `POST /projects`, `DELETE /projects/{id}`, `PUT /projects/{id}`, `DELETE /projects/{id}/{env}` |

## ps

The Swarm tasks of the app's service: replica (slot), state, desired state,
node, container, since when, and the error of one that failed. Without
`--state` the server lists the desired running, complete, shutdown and failed -
a replica's history with its current task. They are shown by replica, the newest
first. A global service's tasks have no replica number: `-`.

## cp

`cp SRC DST`: one side is the container's, `:PATH` for the selected app or
`APP:PATH` for another of the environment; a one-letter prefix is a Windows
drive, not an app.

- **Up.** A file goes as it is: to `PATH`, or into `PATH/` keeping its name. A
  directory goes as a tar the server extracts into `PATH` (`extract`,
  `compressionFormat=tar`), as itself - `site` into `PATH/site` - or, given as
  `site/.`, what is in it: as `docker cp` does. A directory given as a link is
  the tree it points to. Its files are owned by the container's root, as
  `docker cp` gives them, their modes kept.
- **Nothing is replaced by a file.** The CLI sends `overwrite=false`: left out,
  the server lets a file land on a directory of that name and replace it, and
  all it holds. `cp app.conf :/etc/nginx/conf.d` is refused; `:/etc/nginx/conf.d/`
  puts the file in it.
- **Down.** The server answers a file as its bytes and a directory as a tar
  (`application/x-tar`). A file goes to `DST`, or into it when `DST` is a
  directory, under the path's last part. A directory goes into `DST` when `DST`
  is there, and becomes `DST` when it is not, as `docker cp` does. Links are left
  out, and said; an entry outside `DST` stops it.
- **Errors here are said as such.** A local file that cannot be read stops the
  upload with its own error (exit 1), not as a server that could not be reached.
- **Which container.** The server picks a running one. `--replica N` picks the
  running task of slot N from `ps`, and sends its node and container.
- **Size and time.** An upload goes over the upload stream
  (`GET .../container/file-upload/stream`, a websocket), which neither the
  server's timeouts nor Traefik's 60-second read timeout cut, in messages of
  256 KiB; a directory goes gzipped. A server without the stream takes the form,
  and the CLI says that its proxy cuts an upload after 60 seconds. A download
  is not cut while its bytes flow. Progress - MB and rate - goes to stderr at a
  terminal. `--timeout` is the CLI's own wait, 30 minutes by default. See the
  server's [container file transfer design](https://github.com/hivepaas/hivepaas/blob/main/docs/superpowers/specs/2026-10-09-container-file-transfer-design.md).

## job and task

- `job ls`: name, type, schedule (its cron expression, `every 1h`, or `-` for a
  job run only when asked), status, next run.
- `job run JOB`: by name or id, as credentials are named. The run is a task; the
  CLI follows its log over the websocket and polls its status, as for a
  deployment, and exits 0 when it is done, 8 when it failed or was canceled, 9 at
  `--timeout`. Ctrl-C stops the waiting, not the run, and says how to follow it
  (`logs --task`) or cancel it (`task cancel`). A disabled job is refused (exit
  6): the server would take the run and its queue cancel it.
- **A failure the server retries is not the end.** A failed task with a
  `retryAt` runs again: `job run` says so - `The run failed: ... It is tried
  again in 30s.` - and waits for the retry; `logs --task -f` follows it too.
- `job enable|disable JOB`: `PUT .../status` with `status` and `updateVer` only -
  every other field of that request is optional and left as it is. A job in that
  state already is not written.
- `task ls`: id, type (`sched-job-exec`, the server's `task:` dropped), status,
  what it runs (the job, from the list's `targetJob`), created, took. `--type`
  and `--status` filter, `--limit` 20.
- `task get`, `task cancel`: a task not started is canceled at once, one running
  is asked to stop - said apart, as for `deploy cancel`.
- `logs --task ID [-f]`: as `logs --deployment`, which it excludes.

Exit code 8 now reads "what the command waited for - a deployment, a task -
failed or was canceled".

## secret and config-file

Both list the app's own and those it inherits from its project and env (`OF`:
`this app` or `inherited`); `set`, `push` and `rm` touch only its own. A name
the app inherits gets an app's own of that name, not a change of the project's.

**`--scope env` or `--scope project`** acts on the environment's or the
project's instead (`.../{env}/secrets`, `/projects/{id}/secrets`, and the same
for config files): they need `-p`, and `-e` for an env, not `-a`. An
environment's list has the project's it inherits too; messages name "the
environment shop / production" or "the project shop", and the list's column
is `FOR APPS` rather than `PREVIEWS`.

- **Values are never printed.** The server masks a secret's value in lists.
- `secret set KEY=VALUE...` as `env set` does. `secret set KEY` alone reads the
  value from stdin (one trailing newline dropped), or asks for it at a terminal
  without echo: a value on the command line stays in the shell's history.
  `--file` takes a file. A value that is not UTF-8 text goes as `base64`, which
  the server decodes - a keystore stays a keystore.
- `config-file push NAME FILE` (`-` for stdin) and `pull NAME [FILE]` (stdout
  without FILE), from the list's `content`, decoded when `base64`.
- **Shared below.** `inheritable` is the dashboard's "Available in Previews"
  for an app, "Available in Apps" for an env or a project: on for a new one, as
  the dashboard's form starts it, off with `--no-inheritable`; a change keeps
  what the secret had unless a flag says. An app's `--previews` and
  `--no-previews` say the same, and are refused at another scope. A change also sends back
  `default`, which the server overwrites, and the key or name, which it keeps.
- `rm` finds every name before it removes any, each once; a removal that fails
  says which went before it.
- **Every page.** The server answers 50 of a list unless asked, and the spec
  gives these two lists no paging parameters: the CLI asks for pages of 500.
- **A warning is said.** The server can save a change and fail to bring it to
  the apps, and says so in `meta.warning`: so does the CLI.
- A config file reaches a container through a setting mount, made in the
  dashboard; a change reaches the containers on its own.

## project and its environments

- `project create NAME [--envs a,b] [--note]`: development and production unless
  `--envs` says, with the dashboard form's colors; an env added later is slate.
  The owner is left out: the server makes it the key's user.
- `project env add NAME [--color]`: the server has no endpoint for one env; it
  takes the project back with its env list longer. The project is carried over
  whole (`carry.Project`), its owner too - the server sets the owner from the
  request and would blank it.
- `project delete`, `project env rm NAME`: as `app delete` - the name typed at a
  terminal, `--yes` from a script, `--remove-storage` for the volumes' data. The
  server deletes synchronously; the CLI waits up to 10 minutes.
- `--envs`, not `--env`, which names the environment for every command.

## Also in this

- **Exclusive flags exit 2.** cobra checks a flag group past the CLI's flag
  error handler, so `--lfs --no-lfs` and the like exited 1 with cobra's words;
  the root command now reads those errors as usage errors, for every command.
- **Tab** completes the jobs, the tasks, the app's own secrets and its config
  files, and `ps`'s app.
- `resolve.Credential` is `resolve.Setting`: jobs are named the same way.

## Found on the server, not changed here

- `GET .../tasks/{itemID}` loads the task by id alone: its scope is not passed
  (`usecase/taskuc/get.go`), unlike its status, logs and cancel. A key that can
  read one app's tasks can read any task by id through that app's path.
- The spec types `POST .../tasks/{id}/cancel` and `GET .../tasks/{id}/logs` as
  their requests (`taskdto.CancelTaskReq`, `GetTaskLogsReq`); the CLI reads the
  cancel's `{data:{canceled}}` itself.
- The 180-second read and write timeouts, and Traefik's 60-second read timeout,
  ended large container copies: the server's upload stream and its download's
  moving deadline lift them.
- A container file download of a symlink (nginx's `access.log -> /dev/stdout`)
  answers an empty file; a directory download cut short after its headers is
  not told from a whole one, the server's copy errors being dropped.
- The download's `filename*` is escaped with `QueryEscape`, so a space comes as
  `+`; the CLI names the file from the path instead.
- `GET .../secrets` and `.../config-files` take `pageOffset` and `pageLimit` and
  answer 50 by default, but the spec does not say so.
