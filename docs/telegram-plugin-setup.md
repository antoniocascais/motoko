# Telegram plugin setup

cloud-init writes the bot token to the VM, but the Telegram plugin itself needs manual installation. The claude-assistant service restart-loops until you complete these steps.

**Prerequisite:** complete the [post-create setup](../README.md#post-create-setup) first (`claude login` + git HTTPS config).

## 1. Open temporary proxy access

The plugin installer pulls packages from GitHub and npm. Remove these domains when done (step 4).

```bash
motoko proxy add-domain '\.github\.com$'
motoko proxy add-domain '\.githubusercontent\.com$'
motoko proxy add-domain '^marketplace\.claude\.ai$'
motoko proxy add-domain '\.npmjs\.org$'
```

## 2. Install the plugin

```bash
motoko ssh <name>
```

Start an interactive Claude session:

```bash
claude
```

Add the marketplace and install:

```
/plugin
# Select "Add Marketplace" → anthropics/claude-plugins-official

/plugin install telegram@claude-plugins-official
```

Exit the Claude session.

## 3. Install plugin dependencies

`bun install` resolves DNS locally, which fails without DNS. Use npm:

```bash
cd ~/.claude/plugins/marketplaces/claude-plugins-official/external_plugins/telegram
npm install
```

Exit the VM.

## 4. Remove temporary domains

```bash
motoko proxy remove-domain '\.github\.com$'
motoko proxy remove-domain '\.githubusercontent\.com$'
motoko proxy remove-domain '^marketplace\.claude\.ai$'
motoko proxy remove-domain '\.npmjs\.org$'
```

## 5. Restart and verify

```bash
motoko ssh <name> -- sudo -u claude \
  XDG_RUNTIME_DIR=/run/user/\$(id -u claude) \
  systemctl --user restart claude-assistant.service
```

Send a test message to the Telegram bot. If it doesn't respond:

```bash
motoko logs <name>
```
