# What needs attention, previews, backups, and a compose file, from the terminal

Four more commands, each on the endpoints the dashboard uses for the same
thing. It extends [the CLI's design](2026-10-08-cli-design.md); nothing changes
on the server.

| Command | API |
|---|---|
| `status [--fail]` | `GET /home/attention` |
| `preview ls`, `preview create`, `preview rm` | `.../apps/{appID}/previews`, `.../previews/prepare`, `DELETE .../apps/{previewID}` |
| `backup ls\|files\|download\|restore\|rm`, `backup run [JOB]` | `.../apps/{appID}/backup-snapshots...`, `.../sched-jobs/{id}/exec` |
| `compose up [-f FILE]` | `POST /projects/from-compose/{validate,apply}`, `POST /projects/{id}/from-compose/{validate,apply}` |

## status

What Home's "Needs attention" lists, for the whole installation: an app that
is not running or keeps restarting, a node that is down or over-committed. A
row each: severity, kind, where (project / env / app, or the node), since when,
and what is known - replicas running of those asked, restarts, the last error,
memory asked of what a node has. Nothing listed says so. `--fail` exits 1 when
anything is listed, for a script that checks.

## preview

An app's previews: the copies HivePaaS makes of it for a branch or a pull
request, each an app of its own.

- `preview ls [APP]`: the app's previews - name, key, status, updated.
- `preview create [--ref BRANCH] [--subdomain S] [--no-db] [--no-start]`: makes
  one, as the dashboard's form does: first the server says what it can do
  (`prepare`) - previews off for the app is refused, with Feature Settings
  named; the secrets a preview is not given are said - then it is made.
  `--ref` is the branch to build, the app's own when not given; `--no-db` does
  not clone the databases the app uses. The server answers the task that makes
  the preview - clones the app, deploys it - and the command waits for it,
  following its log, as `job run` does (`--no-wait`, `--timeout`).
- `preview rm PREVIEW`: deletes a preview of the app, by name or key, as
  `app delete` deletes an app: asked at a terminal, `--yes` elsewhere. A name
  that is not one of the app's previews is refused, so that `rm` deletes no app
  that is not a preview.

## backup

An app's data backups: snapshots of its volumes, made by its data backup jobs.

- `backup ls [--repo R]`: the snapshots the app sees - time, id, what was backed
  up (volume and path), size, the job that made it, tags; the newest first.
- `backup run [JOB]`: runs a data backup job now and waits for it, as `job run`
  does; the app's one data backup job when there is one, else it is named.
- `backup files SNAPSHOT [PATH]`: what a snapshot holds, at PATH.
- `backup download SNAPSHOT PATH [-O FILE]`: one file of a snapshot, to FILE -
  PATH's last part by default - or `-` for stdout.
- `backup restore SNAPSHOT`: writes a snapshot into the app's volume and waits
  for the task, following its log. `--volume V` and `--subpath S` - where in
  the volume - default to where the snapshot's job backed up, when the data
  goes back into the app it came from;
  `--path P` (a directory of the snapshot alone), `--mode replace|overwrite`
  (replace by default: the directory as it was, the old one kept aside;
  overwrite writes over what is there), `--stop-app` (stopped during it,
  started after), `--to APP` (another app of the environment). It changes data:
  asked at a terminal, `--yes` elsewhere.
- `backup rm SNAPSHOT`: deletes a snapshot from its repository; asked, or
  `--yes`.

A snapshot is given by its id or short id, as `ls` shows them. Restoring a
command's snapshot (a database dump fed to a command) is the dashboard's.

## compose up

`compose up [-f FILE]` makes a project of a Docker Compose file - its services
as apps - or with `-p` adds them to a project's environment, as the dashboard's
import does.

1. **The file:** `-f`, else `compose.yaml`, `compose.yml`, `docker-compose.yaml`
   or `docker-compose.yml` here; the `.env` beside it, or `--env-file`.
2. **What it reads:** the server says the files the compose file needs (an
   `env_file`, a config, a mounted file, an `include`); they are read from
   beside the file and sent with it - a mounted directory's files when they fit
   what the server takes (100 files, 5 MB, 500 KB each), said otherwise. An
   included compose file missing stops it, named; another is said and left out.
3. **Its variables:** `--var K=V` gives one; a required one with no value is
   asked at a terminal - hidden when the server takes it for a secret - and
   refused elsewhere, all of them named.
4. **Where:** without `-p`, a new project, named `--name` or as the file names
   it; with `-p`, its environment `-e` (production by default), made when
   missing. Only `-p` names a project here: a directory's link or
   `HIVEPAAS_PROJECT` does not, so that `compose up` never adds to a project it
   was not told of. `--profile P` adds a profile's services. The services' data
   goes on the project's own volume, as the server picks it.
5. **The plan,** shown before anything is made: each service as the app it
   becomes, its image or build, its ports - how each is reached, at which
   domain - and what is dropped; then the plan's issues by severity. A
   blocking issue stops it (exit 6).
6. **Made,** once confirmed - asked at a terminal, `--yes` elsewhere - with the
   plan's hash: a plan that changed meanwhile is refused. Issues shown are
   accepted. The apps are deployed unless `--no-deploy`, and the command waits
   for them as `template deploy` does (`--no-wait`, `--timeout`).

The choices the dashboard's review offers - another image for a service it
only builds, a port reached otherwise, an existing app used - keep the
server's defaults here; `-o json` shows the plan for a script.

## Exit codes

As for every command: 2 usage, 5 not found, 6 refused by the server's checks or
a blocking issue, 8 a deployment or task that failed, 9 timeout, 130
interrupted; `status --fail` exits 1 when anything needs attention.
