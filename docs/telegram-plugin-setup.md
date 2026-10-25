# Telegram plugin setup

The golden image ships the Telegram plugin (and the `bun` runtime it needs) pre-installed and read-only at `/opt/claude-seed`. cloud-init enables it, points it at a per-instance state directory, and writes the bot token. You only log in and pair your Telegram account.

**Prerequisite:** the instance was created with `--token-env <VAR>` and the golden image was built with this version of motoko (see [Old golden images](#old-golden-images)).

## 1. Log in

```bash
motoko ssh <name>
claude login
```

Then, still in the VM, restart the service so it picks up the credentials:

```bash
systemctl --user restart claude-assistant.service
```

The service runs Claude in a tmux session named `claude`. Attach to it for interactive use (do not start a second `claude --channels`, it would compete for the bot):

```bash
tmux attach -t claude
```

## 2. Pair your Telegram account

1. Send any message to the bot. It replies with a pairing code.
2. In the attached tmux session, run:

```
/telegram:access pair <code>
/telegram:access policy allowlist
```

`allowlist` makes the bot ignore everyone who is not paired. Pairing state lives in `~/data/telegram/access.json` on the data disk and survives `motoko rebuild`.

**Never run `/telegram:configure` in a VM.** It writes the token to a dotenv file in `~/data/telegram/` on the persistent data disk. From then on every interactive `claude` session loads that token, starts its own plugin server and SIGTERMs the service's poller, so the bot goes quiet. The token comes from cloud-init and lives only in `~/.telegram-token` (`0600`), which `claude-run.sh` reads and hands to the service's `claude` alone. It is deliberately not in the systemd unit's environment: the tmux server would keep it in its global environment, and every window you open after `tmux attach` would inherit it. If it happened, delete that dotenv file and restart the service.

For the same reason, never start `claude` from inside the agent's own shell (the agent's Bash tool inherits the token): a second `claude` would take the bot over.

## 3. Verify

Send another message to the bot. If it does not respond:

```bash
motoko logs <name>
```

## What is automated

- Plugin, dependencies and `bun` baked into the golden image (no GitHub or npm access needed at runtime, so no extra proxy domains).
- Plugin enabled through `/etc/claude-code/managed-settings.json`.
- Onboarding and folder-trust dialogs pre-answered in `~/.claude.json`.
- Token stored only in `~/.telegram-token`, a `0600` file owned by the VM user, and passed only to the service's `claude` process (not the systemd unit or tmux environment). If it is missing or empty the service does not start and logs why to `~/data/claude-exit.log`.

## Old golden images

An instance created from a golden image built before this setup existed logs `bun or the plugin seed is missing` to `~/data/claude-exit.log` and the service stays inactive instead of restart-looping (a systemd ExecCondition skips the start). Build a new golden image under a new `golden_image.name` and rebuild the instance (see [Updating Claude Code](../README.md#updating-claude-code)).
