"""SSH Sites MCP: SSH servers, each with multiple site paths.
Every site shows up in the "/" menu; picking one scopes Claude to that server + folder."""
import asyncio, json, os, posixpath, shlex
from pathlib import Path

import paramiko
from mcp.server.fastmcp import FastMCP
from mcp.server.fastmcp.prompts import Prompt

CONFIG = Path(os.environ.get("SSH_SITES_CONFIG", Path(__file__).with_name("sites.json"))).expanduser()
if not CONFIG.exists():
    raise SystemExit(f"Config file not found: {CONFIG}. Please create sites.json (see sites.example.json).")

try:
    content = CONFIG.read_text(encoding="utf-8").strip()
    if not content:
        raise SystemExit(f"Config file {CONFIG} is empty. Please add your servers configuration (see sites.example.json).")
    cfg = json.loads(content)
except json.JSONDecodeError as e:
    raise SystemExit(f"Failed to parse JSON in {CONFIG}: {e}")

SITES = {}  # site name -> {server, root}
for sname, s in cfg.get("servers", {}).items():
    for site, root in s.get("sites", {}).items():
        if site in SITES:
            raise SystemExit(f"Duplicate site name: {site}")
        SITES[site] = {"server": sname, "root": root.rstrip("/") or "/"}

mcp = FastMCP("ssh-sites", host=os.environ.get("HOST", "127.0.0.1"), port=int(os.environ.get("PORT", "8000")))
_clients = {}


def _client(server):
    c = _clients.get(server)
    if c and c.get_transport() and c.get_transport().is_active():
        return c
    s = cfg["servers"][server]
    c = paramiko.SSHClient()
    c.load_system_host_keys()
    c.set_missing_host_key_policy(paramiko.AutoAddPolicy() if s.get("trust_new_host") else paramiko.RejectPolicy())
    
    key_file = os.path.expanduser(s["key_file"]) if s.get("key_file") else None
    pw = s.get("password")
    if pw is None and s.get("password_env"):
        pw = os.environ.get(s["password_env"])

    connect_kwargs = {
        "hostname": s["host"],
        "port": s.get("port", 22),
        "username": s["user"],
        "key_filename": key_file,
        "password": pw,
        "timeout": 15,
        "allow_agent": s.get("allow_agent", False if (pw and not key_file) else True),
        "look_for_keys": s.get("look_for_keys", False if (pw and not key_file) else True),
    }
    c.connect(**connect_kwargs)
    _clients[server] = c
    return c


def _site(site):
    if site not in SITES:
        raise ValueError(f"Unknown site '{site}'. Known: {', '.join(SITES)}")
    info = SITES[site]
    return info["server"], info["root"]


def _confine(root, rel):
    p = posixpath.normpath(posixpath.join(root, rel))
    if p != root and not p.startswith(root.rstrip("/") + "/"):
        raise ValueError("Path escapes the site folder")
    return p


def _readonly(server):
    return bool(cfg["servers"][server].get("readonly"))


def _exec(server, cmd, timeout):
    _, out, err = _client(server).exec_command(cmd, timeout=timeout)
    o, e = out.read().decode(errors="replace"), err.read().decode(errors="replace")
    code = out.channel.recv_exit_status()
    text = f"exit code: {code}\n--- stdout ---\n{o}\n--- stderr ---\n{e}"
    return text if len(text) < 20000 else text[:20000] + "\n...[truncated]"


@mcp.tool()
async def list_sites() -> str:
    """List all configured sites with their server and folder."""
    return "\n".join(f"{n}: {i['server']} ({cfg['servers'][i['server']]['host']}) -> {i['root']}"
                     f"{' [readonly]' if _readonly(i['server']) else ''}" for n, i in SITES.items())


@mcp.tool()
async def run_command(site: str, command: str, timeout: int = 60) -> str:
    """Run a shell command on the site's server, starting in the site's folder."""
    server, root = _site(site)
    if _readonly(server):
        return "This server is readonly; run_command is disabled. Use read_file / list_dir."
    return await asyncio.to_thread(_exec, server, f"cd {shlex.quote(root)} && {command}", timeout)


@mcp.tool()
async def list_dir(site: str, path: str = ".") -> str:
    """List a directory inside the site folder."""
    server, root = _site(site)
    return await asyncio.to_thread(_exec, server, f"ls -la {shlex.quote(_confine(root, path))}", 30)


@mcp.tool()
async def read_file(site: str, path: str) -> str:
    """Read a text file inside the site folder."""
    server, root = _site(site)
    return await asyncio.to_thread(_exec, server, f"cat {shlex.quote(_confine(root, path))}", 30)


@mcp.tool()
async def write_file(site: str, path: str, content: str) -> str:
    """Write (overwrite) a text file inside the site folder."""
    server, root = _site(site)
    if _readonly(server):
        return "This server is readonly."
    full = _confine(root, path)

    def _w():
        sftp = _client(server).open_sftp()
        with sftp.open(full, "w") as f:
            f.write(content)
        sftp.close()
        return f"Wrote {len(content)} bytes to {full}"
    return await asyncio.to_thread(_w)


def _register_prompt(site, info):
    server = info["server"]
    text = (f"You are now working on the site '{site}' on SSH server '{server}' "
            f"({cfg['servers'][server]['host']}). Site folder: {info['root']}.\n"
            f"Use the ssh-sites tools with site=\"{site}\" for every action. "
            f"Do not touch any other site or folder. Confirm before destructive commands "
            f"(rm, drop, restart, overwrite). What I want to do:")
    mcp.add_prompt(Prompt.from_function(lambda: text, name=site,
                                        description=f"{server} · {info['root']}"))


for _n, _i in SITES.items():
    _register_prompt(_n, _i)

if __name__ == "__main__":
    mcp.run(transport=os.environ.get("MCP_TRANSPORT", "stdio"))
