package conf

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"say/network"
	"strconv"
	"strings"
)

var Verbose bool

var colorFilterKeys = []string{"red", "orange", "yellow", "green", "teal", "blue", "purple", "pink", "gray", "bw"}

var colorFilterSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(colorFilterKeys))
	for _, key := range colorFilterKeys {
		m[key] = struct{}{}
	}
	return m
}()

// Friend represents a contact from config: Name + Yggdrasil IPv6 address
type Friend struct {
	Name    string `json:"name"`
	Address string `json:"address"` // Ygg IPv6 like: 200:...:....:....
}

// resolveConfigPath normalizes the config file path, expanding "~", converting it
// to an absolute path, and ensuring the parent directory exists. When cfg is empty,
// it defaults to $XDG_CONFIG_HOME/say/config.json or ~/.config/say/config.json. If
// cfg is a bare filename without an extension (e.g. "profile"), it is treated as
// profile name inside the default config directory ("profile.json").
func resolveConfigPath(cfg string) (string, error) {
	raw := strings.TrimSpace(cfg)

	switch {
	case raw == "":
		if dir, err := defaultConfigDir(); err == nil {
			raw = filepath.Join(dir, "config.json")
		} else {
			raw = "config.json"
		}
	case filepath.Base(raw) == raw && filepath.Ext(raw) == "":
		if dir, err := defaultConfigDir(); err == nil {
			raw = filepath.Join(dir, raw+".json")
		} else {
			raw = raw + ".json"
		}
	}

	cfg = raw

	// Expand leading ~ if user passed it
	if strings.HasPrefix(cfg, "~/") {
		h, err := os.UserHomeDir()
		if err == nil {
			cfg = filepath.Join(h, cfg[2:])
		}
	}
	abs, err := filepath.Abs(cfg)
	if err == nil {
		cfg = abs
	}
	// Ensure parent directory exists
	dir := filepath.Dir(cfg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return cfg, nil
}

// ParseDialTarget parses a dial target string and returns (host, port).
// Supported forms:
//  1. Raw IPv6 without port: "200:...:..."         → (host, defPort)
//  2. Bracketed IPv6 with port: "[200:...]:9999"   → (host, 9999)
//  3. Hostname/IPv4 without port: "host"           → (host, defPort)
//  4. Hostname/IPv4 with port: "host:1234"         → (host, 1234)
//
// For IPv6 with a port, brackets are required.
func ParseDialTarget(raw string, defPort int) (string, int, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", 0, fmt.Errorf("empty dial target")
	}
	// Bracketed form (IPv6 with optional port)
	if strings.HasPrefix(s, "[") {
		// Try [addr]:port first
		if h, p, err := net.SplitHostPort(s); err == nil {
			pi, err := strconv.Atoi(p)
			if err != nil {
				return "", 0, fmt.Errorf("bad port in dial target: %v", err)
			}
			return strings.Trim(h, "[]"), pi, nil
		}
		// Just [addr] without port
		return strings.Trim(s, "[]"), defPort, nil
	}
	// host:port (IPv4 or hostname)
	if strings.Count(s, ":") == 1 && !strings.Contains(s, " ") {
		if h, p, err := net.SplitHostPort(s); err == nil {
			pi, err := strconv.Atoi(p)
			if err != nil {
				return "", 0, fmt.Errorf("bad port in dial target: %v", err)
			}
			return h, pi, nil
		}
	}
	// Raw IPv6 or bare host without port → use default port
	return strings.Trim(s, "[]"), defPort, nil
}

// LoadFriendsFromConfig reads the user config file and extracts the optional "friends"
// array containing known peers. It does not modify or influence how Yggdrasil itself
// interprets the same config file.
func LoadFriendsFromConfig(cfgPath string) ([]Friend, error) {
	if cfgPath == "" {
		return nil, nil
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	var out []Friend
	if v, ok := raw["friends"]; ok && v != nil {
		bs, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(bs, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// HostnameOr returns the system hostname or the provided default string on failure.
func HostnameOr(def string) string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return def
	}
	return h
}

// AppOptions aggregates all CLI flags and configuration options required by the application.
type AppOptions struct {
	Verbose     bool
	ConfigPath  string
	ListenPort  int
	DialAddr    string
	ColorFilter string
}

// ParseCLI parses command-line flags into an AppOptions structure and resolves
// the final configuration path. It performs only argument parsing and normalization.
func ParseCLI() (*AppOptions, error) {
	opts := &AppOptions{}

	flag.BoolVar(&opts.Verbose, "v", false, "enable verbose logging")
	flag.StringVar(&opts.ConfigPath, "config", "", "path to config.json (empty = auto)")
	flag.IntVar(&opts.ListenPort, "port", network.DefaultListenPort, "TCP/UDP port for incoming p2p")
	colorFlagPtrs := make(map[string]*bool, len(colorFilterKeys))
	for _, key := range colorFilterKeys {
		colorFlagPtrs[key] = flag.Bool(key, false, fmt.Sprintf("render viewport with %s tint", key))
	}
	flag.Parse()

	for _, key := range colorFilterKeys {
		if ptr := colorFlagPtrs[key]; ptr != nil && *ptr {
			opts.ColorFilter = key
		}
	}

	extraArgs := flag.Args()
	if filter, remaining := extractColorFilterArg(extraArgs); filter != "" {
		opts.ColorFilter = filter
		extraArgs = remaining
	}
	if opts.ConfigPath == "" {
		if cfg, remaining := extractConfigArg(extraArgs); cfg != "" {
			opts.ConfigPath = cfg
			extraArgs = remaining
		}
	}

	if port, remaining := extractListenPortArg(extraArgs); port > 0 {
		opts.ListenPort = port
		extraArgs = remaining
	}

	opts.DialAddr = parseDialArgv(extraArgs)

	// Normalize config path
	resolvedCfg, err := resolveConfigPath(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("config path error: %w", err)
	}
	opts.ConfigPath = resolvedCfg

	Verbose = opts.Verbose
	return opts, nil
}

func parseDialArgv(args []string) string {
	if len(args) == 0 {
		return ""
	}
	if len(args) == 1 {
		return strings.TrimSpace(args[0])
	}

	var (
		host string
		port string
	)

	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if host == "" && !looksLikePort(arg) {
			host = arg
			continue
		}
		if port == "" && looksLikePort(arg) {
			port = arg
			continue
		}
		if host == "" {
			host = arg
		}
	}

	if host == "" {
		return strings.TrimSpace(args[0])
	}
	if port == "" {
		return host
	}
	return stringifyHostPort(host, port)
}

func looksLikePort(s string) bool {
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, "[]:") {
		return false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return false
	}
	return n > 0 && n <= 65535
}

func stringifyHostPort(host, port string) string {
	h := strings.TrimSpace(host)
	p := strings.TrimSpace(port)
	if h == "" || p == "" {
		return h
	}

	raw := strings.Trim(h, "[]")
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		return fmt.Sprintf("%s:%s", h, p)
	}
	if ip := net.ParseIP(raw); ip != nil && ip.To4() == nil && strings.Contains(raw, ":") {
		return fmt.Sprintf("[%s]:%s", raw, p)
	}
	if strings.Contains(h, ":") {
		return fmt.Sprintf("[%s]:%s", raw, p)
	}
	return fmt.Sprintf("%s:%s", h, p)
}

func extractColorFilterArg(args []string) (string, []string) {
	if len(args) == 0 {
		return "", args
	}
	filtered := make([]string, 0, len(args))
	var filter string
	for _, arg := range args {
		normalized := normalizeColorFlag(arg)
		if normalized != "" {
			filter = normalized
			continue
		}
		filtered = append(filtered, arg)
	}
	return filter, filtered
}

func normalizeColorFlag(arg string) string {
	trimmed := strings.TrimSpace(arg)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "-") {
		return ""
	}
	for len(trimmed) > 0 && trimmed[0] == '-' {
		trimmed = trimmed[1:]
	}
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ToLower(trimmed)
	if _, ok := colorFilterSet[trimmed]; ok {
		return trimmed
	}
	return ""
}

func defaultConfigDir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "say"), nil
}

func extractConfigArg(args []string) (string, []string) {
	if len(args) == 0 {
		return "", args
	}
	first := strings.TrimSpace(args[0])
	if first == "" {
		return "", args[1:]
	}
	if strings.HasPrefix(first, "-") {
		return "", args
	}

	if strings.ContainsRune(first, os.PathSeparator) || strings.Contains(first, "/") || strings.HasSuffix(first, ".json") {
		return first, args[1:]
	}
	if looksLikePort(first) || looksLikeAddress(first) {
		return "", args
	}
	return first, args[1:]
}

func extractListenPortArg(args []string) (int, []string) {
	if len(args) != 1 {
		return 0, args
	}
	token := strings.TrimSpace(args[0])
	if !looksLikePort(token) {
		return 0, args
	}
	port, err := strconv.Atoi(token)
	if err != nil {
		return 0, args
	}
	return port, args[:0]
}

func looksLikeAddress(token string) bool {
	if token == "" {
		return false
	}
	return strings.ContainsAny(token, ":[].")
}
