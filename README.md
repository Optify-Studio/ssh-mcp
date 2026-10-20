# SSH Sites MCP

Add SSH servers, give each one multiple site folders. Every site appears in the `/` menu.
Pick one, then ask anything: Claude works only on that server and folder.

---

## ⚡ Quick Start (Go Binary)

Single, ultra-fast compiled binary with **native passkey / Touch ID / macOS Keychain / 1Password / SSH Agent** support. Zero Python runtime or virtual environment required!

### 1. Build
```bash
# Fetch dependencies and compile
go mod tidy
go build -ldflags="-s -w" -o ssh-sites main.go
```

To cross-compile for all platforms:
```bash
make build-all
# Binaries are saved to dist/:
# - ssh-sites-darwin-arm64 (Apple Silicon M1/M2/M3/M4)
# - ssh-sites-darwin-amd64 (Intel Mac)
# - ssh-sites-linux-amd64 (Linux x86_64)
# - ssh-sites-linux-arm64 (Linux ARM64)
# - ssh-sites-windows-amd64.exe (Windows x86_64)
```

### 2. Configure `sites.json`
```bash
cp sites.example.json sites.json
```

### 3. Connect to Claude

**Claude Desktop** (`claude_desktop_config.json`):
```json
{
  "mcpServers": {
    "ssh-sites": {
      "command": "/full/path/to/ssh-sites",
      "env": {
        "SSH_SITES_CONFIG": "/full/path/to/sites.json"
      }
    }
  }
}
```

**Claude Code:**
```bash
claude mcp add ssh-sites -e SSH_SITES_CONFIG=/full/path/sites.json -- /full/path/to/ssh-sites
```

---

## Config options (per server in `sites.json`)

| Key | Meaning |
|---|---|
| *(none / default)* | **Native Passkey / Touch ID / SSH Agent** (`$SSH_AUTH_SOCK`) |
| `use_agent` | Explicitly enforce SSH agent / passkey authentication |
| `key_file` | Path to private key file (e.g. `~/.ssh/id_ed25519`) |
| `password` | SSH password (plaintext) |
| `password_env` | Environment variable name holding the password |
| `readonly` | `true` disables `run_command` and `write_file` |
| `trust_new_host` | Auto-accept unknown host keys |

---

## Safety notes

- `run_command` starts in the site folder, but a shell can `cd` elsewhere. For real isolation, use a dedicated limited SSH user per server, not root.
- `read_file`, `write_file`, and `list_dir` are strictly confined to the site folder.
- Keep `sites.json` out of git.
