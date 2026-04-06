# Motoko

Go CLI + Ansible for provisioning KVM-isolated sandboxes running Claude Code autonomously.
Ephemeral qcow2 overlays, tinyproxy egress filtering, cloud-init provisioning, Telegram bot per instance.

## Build & Test

Use the Makefile. Run `make help` to see available targets.

## Public Repo Standards

This is a public MIT-licensed repo meant to be cloned and run by anyone.

- NEVER commit secrets, credentials, API keys, tokens, or sensitive data
- NEVER commit custom/user-specific configuration — not even encrypted. All user config must live outside the repo (env vars, local config files in `.gitignore`, etc.)
- Commit messages: clear, professional, focus on WHY not what. Assume public audience.
- Documentation: write for someone cloning this fresh with no prior context
- Keep `.gitignore` current — sensitive file patterns must be excluded before they can be accidentally committed

## Security Rules

Non-negotiable — violating any of these is a critical bug:

- NEVER use `sh -c` or string interpolation for shell commands. Instance names are user input — always use `os/exec` with explicit arg lists.
- NEVER modify the golden base image at runtime. Rebuilds create a new qcow2 overlay.
- VMs have no DNS. All egress routes through tinyproxy on the host. Do not add DNS config to cloud-init or networking setup.
- cloud-init is the ONLY provisioning vector for system-level config (packages, networking, systemd units, DNS disable, proxy setup). No SSH-based config management post-boot. Post-boot SSH is expected for: (a) Claude Code setup that can't be automated (claude login, plugin install, telegram setup), and (b) runtime interaction (logs, inject, tmux).

## Non-Obvious Constraints

- `virsh` needs libvirt group membership, not hardcoded `sudo`
- qcow2 overlay backing file paths must be absolute
- cloud-init NoCloud ISO volume label must be `cidata` (lowercase, exact)
- Go templates use `text/template`, not `html/template` — cloud-init is not HTML
- Ansible `community.libvirt` collection required
