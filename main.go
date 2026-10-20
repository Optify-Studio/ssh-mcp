package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/melbahja/goph"
	"golang.org/x/crypto/ssh"
)

type ServerConfig struct {
	Host         string            `json:"host"`
	Port         int               `json:"port"`
	User         string            `json:"user"`
	KeyFile      string            `json:"key_file"`
	Password     string            `json:"password"`
	PasswordEnv  string            `json:"password_env"`
	UseAgent     bool              `json:"use_agent"`
	Readonly     bool              `json:"readonly"`
	TrustNewHost bool              `json:"trust_new_host"`
	Sites        map[string]string `json:"sites"`
}

type Config struct {
	Servers map[string]ServerConfig `json:"servers"`
}

type SiteInfo struct {
	Server string
	Root   string
}

type ClientManager struct {
	mu      sync.Mutex
	clients map[string]*goph.Client
	cfg     *Config
}

func NewClientManager(cfg *Config) *ClientManager {
	return &ClientManager{
		clients: make(map[string]*goph.Client),
		cfg:     cfg,
	}
}

func expandUser(p string) string {
	if strings.HasPrefix(p, "~/") || p == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func quoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (cm *ClientManager) connect(s ServerConfig) (*goph.Client, error) {
	port := s.Port
	if port == 0 {
		port = 22
	}

	var auth goph.Auth
	var err error

	if s.UseAgent {
		auth, err = goph.UseAgent()
		if err != nil {
			return nil, fmt.Errorf("failed to connect to SSH agent: %w", err)
		}
	} else if s.Password != "" {
		auth = goph.Password(s.Password)
	} else if s.PasswordEnv != "" && os.Getenv(s.PasswordEnv) != "" {
		auth = goph.Password(os.Getenv(s.PasswordEnv))
	} else if s.KeyFile != "" {
		keyPath := expandUser(s.KeyFile)
		auth, err = goph.Key(keyPath, "")
		if err != nil {
			return nil, fmt.Errorf("failed to load private key %s: %w", keyPath, err)
		}
	} else {
		// Default to native SSH Agent / Passkey (macOS Keychain, Touch ID, 1Password)
		auth, err = goph.UseAgent()
		if err != nil {
			return nil, fmt.Errorf("no password or key_file provided, and SSH agent unavailable: %w", err)
		}
	}

	var callback ssh.HostKeyCallback
	if s.TrustNewHost {
		callback = ssh.InsecureIgnoreHostKey()
	} else {
		callback, err = goph.DefaultKnownHosts()
		if err != nil {
			return nil, fmt.Errorf("failed to load known_hosts: %w", err)
		}
	}

	return goph.NewConn(&goph.Config{
		User:     s.User,
		Addr:     s.Host,
		Port:     uint(port),
		Auth:     auth,
		Callback: callback,
		Timeout:  15 * time.Second,
	})
}

func (cm *ClientManager) Get(serverName string) (*goph.Client, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if c, ok := cm.clients[serverName]; ok && c != nil {
		return c, nil
	}

	s, ok := cm.cfg.Servers[serverName]
	if !ok {
		return nil, fmt.Errorf("unknown server '%s'", serverName)
	}

	client, err := cm.connect(s)
	if err != nil {
		return nil, err
	}

	cm.clients[serverName] = client
	return client, nil
}

func (cm *ClientManager) Close(serverName string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if c, ok := cm.clients[serverName]; ok && c != nil {
		_ = c.Close()
		delete(cm.clients, serverName)
	}
}

func (cm *ClientManager) execCmd(ctx context.Context, serverName string, cmdStr string, timeout time.Duration) (string, error) {
	client, err := cm.Get(serverName)
	if err != nil {
		return "", err
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd, err := client.CommandContext(execCtx, "sh", "-c", cmdStr)
	if err != nil {
		cm.Close(serverName)
		client, err = cm.Get(serverName)
		if err != nil {
			return "", err
		}
		cmd, err = client.CommandContext(execCtx, "sh", "-c", cmdStr)
		if err != nil {
			return "", err
		}
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	var exitCode int
	if runErr != nil {
		if exitErr, ok := runErr.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		} else {
			cm.Close(serverName)
			return "", fmt.Errorf("command execution error: %w", runErr)
		}
	}

	out := stdoutBuf.String()
	errStr := stderrBuf.String()
	text := fmt.Sprintf("exit code: %d\n--- stdout ---\n%s\n--- stderr ---\n%s", exitCode, out, errStr)
	if len(text) > 20000 {
		text = text[:20000] + "\n...[truncated]"
	}
	return text, nil
}

func (cm *ClientManager) writeFile(serverName, fullPath, content string) (string, error) {
	client, err := cm.Get(serverName)
	if err != nil {
		return "", err
	}

	sftpClient, err := client.NewSftp()
	if err != nil {
		cm.Close(serverName)
		client, err = cm.Get(serverName)
		if err != nil {
			return "", err
		}
		sftpClient, err = client.NewSftp()
		if err != nil {
			return "", fmt.Errorf("failed to start sftp: %w", err)
		}
	}
	defer sftpClient.Close()

	dir := path.Dir(fullPath)
	if dir != "." && dir != "/" {
		_ = sftpClient.MkdirAll(dir)
	}

	f, err := sftpClient.Create(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to create remote file: %w", err)
	}
	defer f.Close()

	n, err := f.Write([]byte(content))
	if err != nil {
		return "", fmt.Errorf("failed to write remote file: %w", err)
	}

	return fmt.Sprintf("Wrote %d bytes to %s", n, fullPath), nil
}

func confine(root, rel string) (string, error) {
	cleanRoot := path.Clean(root)
	var target string
	if path.IsAbs(rel) {
		target = path.Clean(rel)
	} else {
		target = path.Clean(path.Join(cleanRoot, rel))
	}
	if target != cleanRoot && !strings.HasPrefix(target, strings.TrimSuffix(cleanRoot, "/")+"/") {
		return "", fmt.Errorf("path escapes the site folder")
	}
	return target, nil
}

func findConfigFile() (string, error) {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		p := expandUser(os.Args[1])
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	if env := os.Getenv("SSH_SITES_CONFIG"); env != "" {
		p := expandUser(env)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("config file not found at SSH_SITES_CONFIG: %s", p)
	}

	if _, err := os.Stat("sites.json"); err == nil {
		return "sites.json", nil
	}

	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "sites.json")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("config file not found: sites.json. Please create sites.json (see sites.example.json)")
}

func getStringArg(req mcp.CallToolRequest, name string) (string, error) {
	val, ok := req.Params.Arguments[name]
	if !ok || val == nil {
		return "", fmt.Errorf("missing required argument '%s'", name)
	}
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("argument '%s' must be a string", name)
	}
	return s, nil
}

func getOptionalStringArg(req mcp.CallToolRequest, name string, def string) string {
	val, ok := req.Params.Arguments[name]
	if !ok || val == nil {
		return def
	}
	if s, ok := val.(string); ok && s != "" {
		return s
	}
	return def
}

func getOptionalIntArg(req mcp.CallToolRequest, name string, def int) int {
	val, ok := req.Params.Arguments[name]
	if !ok || val == nil {
		return def
	}
	switch v := val.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i)
		}
	}
	return def
}

func main() {
	cfgPath, err := findConfigFile()
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	content, err := os.ReadFile(cfgPath)
	if err != nil {
		log.Fatalf("Failed to read config file %s: %v", cfgPath, err)
	}

	if len(strings.TrimSpace(string(content))) == 0 {
		log.Fatalf("Config file %s is empty. Please add your servers configuration (see sites.example.json).", cfgPath)
	}

	var cfg Config
	if err := json.Unmarshal(content, &cfg); err != nil {
		log.Fatalf("Failed to parse JSON in %s: %v", cfgPath, err)
	}

	sites := make(map[string]SiteInfo)
	for sname, s := range cfg.Servers {
		for siteName, root := range s.Sites {
			if _, exists := sites[siteName]; exists {
				log.Fatalf("Duplicate site name: %s", siteName)
			}
			cleanRoot := strings.TrimRight(root, "/")
			if cleanRoot == "" {
				cleanRoot = "/"
			}
			sites[siteName] = SiteInfo{
				Server: sname,
				Root:   cleanRoot,
			}
		}
	}

	clientMgr := NewClientManager(&cfg)

	s := server.NewMCPServer(
		"ssh-sites",
		"1.0.0",
		server.WithPromptCapabilities(false),
	)

	// Tool: list_sites
	s.AddTool(
		mcp.NewTool("list_sites",
			mcp.WithDescription("List all configured sites with their server and folder."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var lines []string
			for siteName, info := range sites {
				serverCfg := cfg.Servers[info.Server]
				roTag := ""
				if serverCfg.Readonly {
					roTag = " [readonly]"
				}
				lines = append(lines, fmt.Sprintf("%s: %s (%s) -> %s%s", siteName, info.Server, serverCfg.Host, info.Root, roTag))
			}
			return mcp.NewToolResultText(strings.Join(lines, "\n")), nil
		},
	)

	// Tool: run_command
	s.AddTool(
		mcp.NewTool("run_command",
			mcp.WithDescription("Run a shell command on the site's server, starting in the site's folder."),
			mcp.WithString("site", mcp.Required(), mcp.Description("The site identifier")),
			mcp.WithString("command", mcp.Required(), mcp.Description("The shell command to run")),
			mcp.WithNumber("timeout", mcp.Description("Timeout in seconds (default 60)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			siteName, err := getStringArg(req, "site")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			command, err := getStringArg(req, "command")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			timeoutSec := getOptionalIntArg(req, "timeout", 60)

			info, ok := sites[siteName]
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("Unknown site '%s'", siteName)), nil
			}

			if cfg.Servers[info.Server].Readonly {
				return mcp.NewToolResultText("This server is readonly; run_command is disabled. Use read_file / list_dir."), nil
			}

			shCmd := fmt.Sprintf("cd %s && %s", quoteArg(info.Root), command)
			res, err := clientMgr.execCmd(ctx, info.Server, shCmd, time.Duration(timeoutSec)*time.Second)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(res), nil
		},
	)

	// Tool: list_dir
	s.AddTool(
		mcp.NewTool("list_dir",
			mcp.WithDescription("List a directory inside the site folder."),
			mcp.WithString("site", mcp.Required(), mcp.Description("The site identifier")),
			mcp.WithString("path", mcp.Description("Relative path within site folder (default '.')")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			siteName, err := getStringArg(req, "site")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			relPath := getOptionalStringArg(req, "path", ".")

			info, ok := sites[siteName]
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("Unknown site '%s'", siteName)), nil
			}

			target, err := confine(info.Root, relPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			shCmd := fmt.Sprintf("ls -la %s", quoteArg(target))
			res, err := clientMgr.execCmd(ctx, info.Server, shCmd, 30*time.Second)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(res), nil
		},
	)

	// Tool: read_file
	s.AddTool(
		mcp.NewTool("read_file",
			mcp.WithDescription("Read a text file inside the site folder."),
			mcp.WithString("site", mcp.Required(), mcp.Description("The site identifier")),
			mcp.WithString("path", mcp.Required(), mcp.Description("Relative path to the file")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			siteName, err := getStringArg(req, "site")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			relPath, err := getStringArg(req, "path")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			info, ok := sites[siteName]
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("Unknown site '%s'", siteName)), nil
			}

			target, err := confine(info.Root, relPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			shCmd := fmt.Sprintf("cat %s", quoteArg(target))
			res, err := clientMgr.execCmd(ctx, info.Server, shCmd, 30*time.Second)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(res), nil
		},
	)

	// Tool: write_file
	s.AddTool(
		mcp.NewTool("write_file",
			mcp.WithDescription("Write (overwrite) a text file inside the site folder."),
			mcp.WithString("site", mcp.Required(), mcp.Description("The site identifier")),
			mcp.WithString("path", mcp.Required(), mcp.Description("Relative path to the file")),
			mcp.WithString("content", mcp.Required(), mcp.Description("File content to write")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			siteName, err := getStringArg(req, "site")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			relPath, err := getStringArg(req, "path")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			content, err := getStringArg(req, "content")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			info, ok := sites[siteName]
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("Unknown site '%s'", siteName)), nil
			}

			if cfg.Servers[info.Server].Readonly {
				return mcp.NewToolResultText("This server is readonly."), nil
			}

			target, err := confine(info.Root, relPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			res, err := clientMgr.writeFile(info.Server, target, content)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(res), nil
		},
	)

	// Register Prompt for each site
	for name, info := range sites {
		siteName := name
		siteInfo := info
		s.AddPrompt(
			mcp.NewPrompt(
				siteName,
				mcp.WithPromptDescription(fmt.Sprintf("%s · %s", siteInfo.Server, siteInfo.Root)),
			),
			func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				text := fmt.Sprintf("You are now working on the site '%s' on SSH server '%s' (%s). Site folder: %s.\n"+
					"Use the ssh-sites tools with site=\"%s\" for every action. "+
					"Do not touch any other site or folder. Confirm before destructive commands "+
					"(rm, drop, restart, overwrite). What I want to do:",
					siteName, siteInfo.Server, cfg.Servers[siteInfo.Server].Host, siteInfo.Root, siteName)
				return mcp.NewGetPromptResult(
					fmt.Sprintf("Context for %s", siteName),
					[]mcp.PromptMessage{
						mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(text)),
					},
				), nil
			},
		)
	}

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
