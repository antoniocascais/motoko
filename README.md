# motoko

KVM-isolated sandboxes for running Claude Code as an autonomous AI agent.

![motoko lifecycle demo](docs/demo-lifecycle.gif)
*Spin up an isolated Claude Code VM and manage it from the terminal*

## Features

- **Full VM isolation**: each agent runs in its own KVM virtual machine, not a container
- **Network egress filtering**: allowlist specific domains through tinyproxy; no DNS in the VM
- **Stable per-instance IP**: each VM reserves a deterministic address before boot — concurrent creates never race for the same lease
- **Ephemeral compute, persistent data**: root disk resets on rebuild (qcow2 overlay), data disk persists
- **Telegram bot interface**: send prompts and get responses via Telegram using [Claude Code official plugin](https://github.com/anthropics/claude-plugins-official/tree/main/external_plugins/telegram)
- **Single-binary CLI**: lifecycle management, prompt injection, log tailing, proxy rules
- **No local Go needed**: builds via Docker, runs on any Linux KVM host

## Quick start

```bash
# Build the binary (uses Docker, no local Go needed)
make build

# Check prerequisites, generate default config
./dist/motoko init

# Review and edit config
vim ~/.config/motoko/config.yml

# Build the golden base image (downloads Debian cloud image, ~5 min)
./dist/motoko build

# Create an instance (add --token-env for Telegram bot support)
./dist/motoko create my-agent

# Interact
./dist/motoko logs my-agent         # watch Claude Code output
./dist/motoko inject my-agent "fix the tests in src/"
./dist/motoko ssh my-agent           # shell into the VM

# Reset without losing data
./dist/motoko rebuild my-agent

# Tear down
./dist/motoko destroy my-agent --purge
```

## Post-create setup

After `motoko create`, SSH in to authenticate Claude Code and configure git:

```bash
motoko ssh my-agent

# Inside the VM:
claude login

# Force HTTPS for GitHub (VM has no DNS, SSH git clone fails)
git config --global url."https://github.com/".insteadOf "git@github.com:"
git config --global url."https://github.com/".insteadOf "ssh://git@github.com/"

# Restart the service to pick up the new credentials
systemctl --user restart claude-assistant.service
```

The assistant runs in a tmux session: SSH in and run `tmux attach -t claude` for interactive use.
The Telegram plugin is pre-installed; to log in and pair your account, see [docs/telegram-plugin-setup.md](docs/telegram-plugin-setup.md).

## Commands

![motoko interaction demo](docs/demo-interact.gif)
*Inject prompts and read Claude Code output from a running instance*

```
motoko init                          Check prerequisites, create default config
motoko build [--force]               Build golden base image
motoko create <name>                 Create a new instance
  --token-env VAR                      env var with Telegram bot token
  --ssh-key PATH                       operator SSH public key file
  --persona PATH                       persona markdown file
motoko list                          List all instances
motoko ssh <name>                    SSH into an instance
motoko logs <name>                   Show Claude Code tmux output
motoko inject <name> <prompt>        Send a prompt to Claude Code
motoko start <name>                  Start a stopped instance
motoko stop <name>                   Gracefully shut down
motoko rebuild <name> [--yes]        Reset overlay, preserve data disk
motoko update <name>                 Bump RAM/vCPUs of a stopped instance
  --ram <MiB>                          new RAM size (>= 512)
  --cpus <N>                           new vCPU count (>= 1)
motoko destroy <name> [--yes]        Remove instance
  --purge                              also delete data disk, keys, and state
motoko proxy list                    Show proxy domain allowlist
motoko proxy add-domain <pattern>    Add domain regex to allowlist
motoko proxy remove-domain <pattern> Remove domain regex from allowlist
```

## Architecture

```
Host (Linux + KVM)
├── tinyproxy (egress filter, domain allowlist)
├── nftables (VM-to-VM isolation on virbr0)
└── motoko CLI
    ├── Golden image (Debian 12 + packages, built once)
    ├── Instance overlays (ephemeral, per-VM)
    ├── Data disks (persistent, per-VM)
    └── cloud-init ISOs (networking, proxy, Claude setup)

VM (motoko-<name>)
├── Claude Code (tmux session, auto-started via systemd)
├── Telegram plugin (receives prompts, sends responses)
└── Mounted data disk at ~/data
```

## Prerequisites

- Linux host with KVM (`/dev/kvm`)
- libvirt + QEMU: `virsh`, `virt-install`, `virt-customize`, `qemu-img`, `guestfish`
- `cloud-localds` (cloud-image-utils on Debian, cloud-utils on Arch)
- User in `libvirt` group, `libvirtd` running
- libvirt `default` network active
- tinyproxy on port 3128, bound to the bridge IP (default `192.168.122.1`)
- nftables rules isolating VMs from each other on virbr0
- Docker (for building the binary only, no local Go toolchain needed)

## Host setup

Once `motoko init` passes, create the image directories:

```bash
sudo mkdir -p /var/lib/libvirt/images/motoko /var/lib/libvirt/cloud-init/motoko
sudo chown root:libvirt /var/lib/libvirt/images/motoko /var/lib/libvirt/cloud-init/motoko
sudo chmod 775 /var/lib/libvirt/images/motoko /var/lib/libvirt/cloud-init/motoko
```

The proxy filter file (`/etc/tinyproxy/allowed-domains`) must be writable by your user. Check group ownership if `motoko proxy` fails with a permission error.

`motoko init` also writes `~/.config/motoko/env` (`export LIBVIRT_DEFAULT_URI=qemu:///system`). Source it (`source ~/.config/motoko/env`) so interactive `virsh`/`virt-*` debugging targets the same system libvirt instance motoko uses, not your per-user session default.

## Configuration

`~/.config/motoko/config.yml`, generated by `motoko init`. Key settings:

```yaml
golden_image:
  name: debian-12-claude-golden.qcow2
  claude_channel: stable  # stable (about a week behind, skips bad releases) or latest
  claude_version: ""      # optional exact pin, e.g. "2.1.308"; empty = newest in channel
vm_defaults:
  vcpus: 4
  ram_mb: 4096
  data_disk_gb: 10        # persistent disk size
  cpu_pinning: "0-3"      # optional, pin to specific cores
  timezone: UTC
proxy:
  port: 3128
  filter_file: /etc/tinyproxy/allowed-domains
  allowed_domains:        # seeded on motoko init
    - '\.anthropic\.com$'
    - '^api\.telegram\.org$'
```

## Updating Claude Code

`motoko build` installs Claude Code from Anthropic's signed apt repository
(`downloads.claude.ai/claude-code/apt`). The repository signing key ships with motoko
(`pkg/vm/keys/claude-code.asc`, fingerprint `31DD DE24 DDFA B679 F42D 7BD2 BAA9 29FF 1A7E CACE`),
so the build trusts that key and nothing fetched at build time. The download happens during the
image build on the host, not through tinyproxy, so the VM allowlist needs no extra entry. The apt
source and key are removed once the package is installed, so apt inside a VM can neither reach the
repo nor upgrade Claude Code.

Instances never update themselves (`DISABLE_UPDATES=1`): the version in a VM is the one baked
into its golden image. To roll out a new version, keep existing instances safe by building under
a new name, then rebuild each instance (its data disk is preserved):

```bash
# config.yml: golden_image.name: debian-12-claude-golden-2.qcow2
motoko build
motoko rebuild my-agent
```

If the assistant keeps dying, `motoko logs <name>` shows `~/data/claude-exit.log` once the tmux
session is gone: one record per exit, with the exit status and claude's last screen.

## What the golden image bakes

`motoko build` installs software only, nothing per-instance or secret:

- apt packages (`git`, `tmux`, `claude-code` from the signed repo, plus `golden_image.packages`)
- `bun` at `/usr/local/bin/bun`: pinned version and zip sha256 in `pkg/vm/golden_seed.go` (`pinnedBun`), downloaded and verified on the host, never in the VM
- the Telegram plugin seed at `/opt/claude-seed` (root-owned, read-only), installed with `claude plugin marketplace add` and `claude plugin install` inside the build appliance

cloud-init owns all per-instance config: managed settings, the plugin enablement, the token, state directories. Manual: `claude login` and Telegram pairing.

### Bumping the pins

**bun:** pick a release, verify its `SHASUMS256.txt.asc` signature against the Bun release key (fingerprint `F3DC C08A 8572 C074 9B3E  1888 8EAB 4D40 A7B2 2B59`, use an isolated `GNUPGHOME`), then copy the `bun-linux-x64-baseline.zip` line's sha256 and the version into `pinnedBun`.

**Telegram plugin:** the marketplace commit cannot be pinned through the CLI, so the build pins the plugin directory content instead. The bake fails (golden untouched) unless the installed plugin directory, outside `node_modules`, matches `pkg/vm/seed/telegram-plugin.sha256` exactly. After reviewing an upstream plugin change, regenerate the manifest and set `telegramPluginVersion`:

```bash
python3 -I pkg/vm/seed/gen-telegram-manifest.py anthropics/claude-plugins-official <commit> external_plugins/telegram \
  > pkg/vm/seed/telegram-plugin.sha256
```

Run it from the repo root with an authenticated `gh`. For the current pin the invocation is `... anthropics/claude-plugins-official 1e96c6b0a0f06cad576086243f3d06397c4ddf44 external_plugins/telegram`.

**Also re-check on every plugin bump:** in the new `server.ts`, the exit on a missing bot token must still come BEFORE the PID guard that stops an already-running poller. Managed settings enable the plugin for every `claude` session, and interactive shells have no token, so that ordering is the only thing stopping an interactive session from taking the bot away from the service. If the order changed, do not bump until the cloud-init split is rethought.

The current manifest was generated from upstream commit `1e96c6b0a0f06cad576086243f3d06397c4ddf44`. `node_modules` is covered by the plugin's frozen `bun.lock`. The marketplace commit actually installed is printed in the build output (`motoko-seed marketplace-commit`) but not enforced.

## Security model

- VMs have no DNS. You control egress through the host proxy's domain allowlist.
- Instance names are user input. All shell commands use `exec.Command` with explicit arg lists, no `sh -c`.
- The golden image holds software only; cloud-init provisions all per-instance config. Post-boot SSH is for login, pairing and operator interaction, not configuration.
- Each rebuild creates a fresh overlay from the golden image. The golden image is read-only.
- cloud-init ISOs contain the Telegram bot token and are `chmod 0600`.
- Each instance's SSH host keys are pinned at create time (`instances/<name>/known_hosts`), captured over the authenticated connection once identity is already proven by the static IP reservation, hostname assert, and key auth — then enforced on every later `ssh`/`inject`/`logs`. A changed host key (a different VM answering at that IP) fails closed. `rebuild` re-pins, since a fresh overlay regenerates the guest keys.

## License

MIT
