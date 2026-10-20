# SSH Sites MCP

Add SSH servers, give each one multiple site folders. Every site appears in the `/` menu.
Pick one, then ask anything: Claude works only on that server and folder.

## Setup

```bash
cd ssh-sites-mcp
python -m venv .venv && source .venv/bin/activate   # Windows: .venv\Scripts\activate
pip install -r requirements.txt
cp sites.example.json sites.json                     # edit with your servers + site paths
```

Add each server's host key once so connections are verified:
`ssh-keyscan -H your.server.ip >> ~/.ssh/known_hosts`
(or set `"trust_new_host": true` on that server to skip this).

## Connect to Claude

**Claude Desktop** (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "ssh-sites": {
      "command": "/full/path/to/.venv/bin/python",
      "args": ["/full/path/to/server.py"],
      "env": { "SSH_SITES_CONFIG": "/full/path/to/sites.json" }
    }
  }
}
```

**Claude Code:**

```bash
claude mcp add ssh-sites -e SSH_SITES_CONFIG=/full/path/sites.json -- /full/path/.venv/bin/python /full/path/server.py
```

Restart, type `/`, and each site name is listed (description shows server and folder).

## Config options (per server)

| Key | Meaning |
|---|---|
| `key_file` | SSH private key path (e.g. `~/.ssh/id_ed25519`) |
| `password` | SSH password (plaintext in `sites.json`) |
| `password_env` | Name of an env var holding the password |
| `readonly` | `true` disables `run_command` and `write_file` |
| `trust_new_host` | Auto-accept unknown host keys |

## Safety notes

- `run_command` starts in the site folder, but a shell can `cd` elsewhere. For real isolation,
  use a dedicated limited SSH user per server, not root.
- `read_file`, `write_file`, and `list_dir` are strictly confined to the site folder.
- Keep `sites.json` out of git.
