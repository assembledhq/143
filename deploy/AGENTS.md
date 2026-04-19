# Deploy scripts

These scripts provision and deploy 143 to the VPS fleet via SSH. Most of them ship a bash heredoc over an SSH connection to run commands on a remote host. Two failure modes have bitten us and keep masquerading as successful deploys — guard against both when writing or reviewing scripts here.

## Rule 1: every SSH heredoc must start with `set -euo pipefail`

`set -euo pipefail` in the outer script does **not** carry into a remote heredoc — the remote `bash` starts fresh. Without it, any failure inside the heredoc (a failed `docker pull`, a broken `docker compose` config, a migration error) is silently ignored, and the script exits 0 because the *last* command — usually a success `echo` — ran fine. The outer fleet script then cheerfully reports `host@ip deployed.`

Every heredoc should look like this:

```bash
ssh "${SSH_OPTS[@]}" deploy@"$HOST" << 'REMOTE'
  set -euo pipefail
  # ... real work ...
REMOTE
```

This applies to **every** heredoc, even short ones with a single command. A one-line heredoc today is a two-line heredoc tomorrow, and the day it grows is the day a silent failure gets shipped.

## Rule 2: commands that attach stdin must redirect from `/dev/null`

An SSH heredoc sends its body to the remote `bash` on stdin. Any command inside the heredoc that itself attaches stdin will drain the rest of the heredoc as input, and `bash` hits EOF early — exiting 0 with every subsequent line silently skipped.

The docker CLI is the main offender. Any of these will drain the heredoc:

- `docker compose run` (even with `-T`) — we got bit by this in `deploy.sh` migrating
- `docker compose exec` (even with `-T`) — same mechanism
- `docker run -i` / `docker exec -i`
- Anything you pipe into `docker login --password-stdin` (fine only because the pipe provides stdin)

The fix is a one-character redirect:

```bash
docker compose run --rm -T --no-deps api /bin/migrate up < /dev/null
```

Even if the command is the last line of the heredoc today, add `< /dev/null` anyway — the next edit that appends a line will silently regress.

## Quick audit checklist before merging a script

- [ ] Outer script has `set -euo pipefail` at the top
- [ ] Every SSH heredoc has `set -euo pipefail` as its first line
- [ ] Every `docker compose run/exec` and `docker run/exec -i` inside a heredoc has `< /dev/null`
- [ ] The heredoc delimiter is quoted (`<< 'REMOTE'`) unless you deliberately want local variable expansion
- [ ] Commands that are *expected* to fail are guarded with `|| true` (they'll now abort the whole script otherwise)
