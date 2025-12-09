package conf

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"github.com/svanichkin/say/network"
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
	resolved, err := resolveConfigPathRaw(cfg)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return resolved, nil
}

// previewConfigPath resolves the config path without touching the filesystem.
// It is used to test whether a profile already exists before finalizing the choice.
func previewConfigPath(cfg string) (string, error) {
	return resolveConfigPathRaw(cfg)
}

func resolveConfigPathRaw(cfg string) (string, error) {
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

// LoadFriendsFromConfig currently only extracts the optional "contacts_dir" value.
// Inline friend definitions have been deprecated in favor of filesystem contacts.
func LoadFriendsFromConfig(cfgPath string) ([]Friend, string, error) {
	if cfgPath == "" {
		return nil, "", nil
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, "", err
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, "", err
	}
	var contactsDir string
	if v, ok := raw["contacts_dir"]; ok && v != nil {
		if s, ok := v.(string); ok {
			contactsDir = strings.TrimSpace(s)
		}
	}
	return nil, contactsDir, nil
}

// LoadFriendsFromContacts recursively scans the provided directory for friend folders.
// Each folder that contains a file named "say" contributes a friend entry where the
// folder name is treated as the friend name and the file contents hold the IPv6 address.
func LoadFriendsFromContacts(root string) ([]Friend, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, nil
	}
	resolved, err := resolvePathAllowingHome(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("contacts path %q is not a directory", resolved)
	}
	var friends []Friend
	seen := make(map[string]struct{})
	err = filepath.WalkDir(resolved, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !strings.EqualFold(d.Name(), "say") {
			return nil
		}
		friendFolder := filepath.Dir(path)
		friendName := filepath.Base(friendFolder)
		if friendName == "" {
			return filepath.SkipDir
		}
		addFriend := func(name, addr string) {
			key := normalizeFriendKey(name)
			if key == "" || addr == "" {
				return
			}
			if _, ok := seen[key]; ok {
				return
			}
			seen[key] = struct{}{}
			friends = append(friends, Friend{Name: name, Address: addr})
		}
		if !d.IsDir() {
			addr, err := readAddressFile(path)
			if err != nil {
				return err
			}
			addFriend(friendName, addr)
			return nil
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		var devices []fs.DirEntry
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			devices = append(devices, entry)
		}
		if len(devices) == 0 {
			return filepath.SkipDir
		}
		if len(devices) == 1 {
			addr, err := readAddressFile(filepath.Join(path, devices[0].Name()))
			if err != nil {
				return err
			}
			addFriend(friendName, addr)
			return filepath.SkipDir
		}
		for _, entry := range devices {
			addr, err := readAddressFile(filepath.Join(path, entry.Name()))
			if err != nil {
				return err
			}
			label := fmt.Sprintf("%s/%s", friendName, entry.Name())
			addFriend(label, addr)
		}
		return filepath.SkipDir
	})
	if err != nil {
		return nil, err
	}
	return friends, nil
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
	FriendName  string
	ContactsDir string
	Mode        AppMode
	ColorFilter string
}

// AppMode describes whether the CLI determined that Say should run as client or server.
type AppMode int

const (
	ModeAuto AppMode = iota
	ModeServer
	ModeClient
)

// ParseCLI parses command-line flags into an AppOptions structure and resolves
// the final configuration path. It performs only argument parsing and normalization.

type flagParseState struct {
	contactsOverride bool
}

// ParseCLI parses command-line flags into an AppOptions structure and resolves
// the final configuration path. It performs only argument parsing and normalization.
func ParseCLI() (*AppOptions, error) {
	opts := &AppOptions{ListenPort: network.DefaultListenPort}

	rawArgs := compactArgs(os.Args[1:])
	noArgs := len(rawArgs) == 0
	flagTokens, consumed := collectDashPrefixedArgs(rawArgs)
	state := &flagParseState{}
	if err := applyFlagTokens(flagTokens, opts, state); err != nil {
		return nil, err
	}
	if noArgs {
		if err := opts.setMode(ModeServer); err != nil {
			return nil, err
		}
		if opts.ConfigPath == "" {
			if dir, err := defaultConfigDir(); err == nil {
				opts.ConfigPath = filepath.Join(dir, "config.json")
			}
		}
	}
	extraArgs := remainingArgs(rawArgs, consumed)
	var filter string
	filter, extraArgs = extractColorFilterArg(extraArgs)
	if filter != "" {
		opts.ColorFilter = filter
	}
	var port int
	port, extraArgs = extractListenPortArg(extraArgs)
	if port > 0 {
		opts.ListenPort = port
	}

	configArg := opts.ConfigPath
	var friendName string
	var pendingArg string
	var pendingDial bool
	remaining := extraArgs
	if len(remaining) > 0 {
		if len(remaining) > 1 {
			return nil, fmt.Errorf("unexpected extra positional arguments: %v", remaining[1:])
		}
		if looksLikePort(remaining[0]) {
			return nil, fmt.Errorf("extra numeric argument %q", remaining[0])
		}
		pendingArg = remaining[0]
		pendingDial = isLikelyDial(pendingArg)
	}

	resolvedCfg, err := resolveConfigPath(configArg)
	if err != nil {
		return nil, fmt.Errorf("config path error: %w", err)
	}
	opts.ConfigPath = resolvedCfg
	var contactsFromCfg string
	if _, cfgDir, err := LoadFriendsFromConfig(resolvedCfg); err == nil {
		contactsFromCfg = strings.TrimSpace(cfgDir)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if opts.ContactsDir != "" {
		if state.contactsOverride {
			persistContactsDir(resolvedCfg, opts.ContactsDir)
		}
	} else if contactsFromCfg != "" {
		opts.ContactsDir = contactsFromCfg
	}
	effectiveContactsDir := opts.ContactsDir
	if pendingArg != "" {
		if pendingDial {
			if opts.DialAddr != "" {
				return nil, fmt.Errorf("dial address already provided")
			}
			opts.DialAddr = pendingArg
			if err := opts.setMode(ModeClient); err != nil {
				return nil, err
			}
		} else {
			if effectiveContactsDir == "" {
				return nil, fmt.Errorf("friend %q requires contacts_dir", pendingArg)
			}
			friendsPreview, err := LoadFriendsFromContacts(effectiveContactsDir)
			if err != nil {
				return nil, err
			}
			if !friendExistsByName(friendsPreview, pendingArg) {
				return nil, fmt.Errorf("friend %q not found in contacts", pendingArg)
			}
			friendName = pendingArg
			if err := opts.setMode(ModeClient); err != nil {
				return nil, err
			}
		}
	}
	opts.FriendName = friendName

	if opts.Mode == ModeAuto {
		if opts.DialAddr == "" && friendName == "" {
			if err := opts.setMode(ModeServer); err != nil {
				return nil, err
			}
		}
	}
	if opts.Mode == ModeAuto {
		return nil, fmt.Errorf("unable to determine mode from arguments")
	}
	if opts.Mode == ModeClient && opts.DialAddr == "" && friendName == "" {
		return nil, fmt.Errorf("client mode requires friend name or dial address")
	}

	Verbose = opts.Verbose
	return opts, nil
}

func (opts *AppOptions) setMode(mode AppMode) error {
	if mode == ModeAuto {
		return nil
	}
	if opts.Mode == ModeAuto || opts.Mode == mode {
		opts.Mode = mode
		return nil
	}
	return fmt.Errorf("conflicting mode request: %v -> %v", opts.Mode, mode)
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

func extractColorFilterArg(args []string) (string, []string) {
	if len(args) == 0 {
		return "", args
	}
	filtered := make([]string, 0, len(args))
	var filter string
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			if normalized := normalizeColorFlag(trimmed); normalized != "" {
				filter = normalized
				continue
			}
			filtered = append(filtered, trimmed)
			continue
		}
		filtered = append(filtered, trimmed)
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

func extractListenPortArg(args []string) (int, []string) {
	if len(args) == 0 {
		return 0, args
	}
	filtered := make([]string, 0, len(args))
	var port int
	for _, raw := range args {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}
		if port == 0 && looksLikePort(token) {
			if p, err := strconv.Atoi(token); err == nil {
				port = p
				continue
			}
		}
		filtered = append(filtered, token)
	}
	return port, filtered
}

func compactArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	out := make([]string, 0, len(args))
	for _, raw := range args {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func extractDialAddressArg(args []string) (string, []string) {
	if len(args) == 0 {
		return "", args
	}
	filtered := make([]string, 0, len(args))
	var dial string
	consumed := false
	for _, token := range args {
		trimmed := strings.TrimSpace(token)
		if trimmed == "" {
			continue
		}
		if !consumed && isLikelyDial(trimmed) {
			dial = trimmed
			consumed = true
			continue
		}
		filtered = append(filtered, trimmed)
	}
	return dial, filtered
}

func isLikelyDial(token string) bool {
	if token == "" {
		return false
	}
	if looksLikePort(token) {
		return false
	}
	if strings.ContainsAny(token, "[]:") {
		return true
	}
	return strings.Contains(token, ".")
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}

func readAddressFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	addr := strings.TrimSpace(string(b))
	return addr, nil
}

func normalizeFriendKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func resolvePathAllowingHome(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		h, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(h, path[2:])
		}
	}
	return filepath.Abs(path)
}

func persistContactsDir(configPath, contactsDir string) {
	if contactsDir == "" || configPath == "" {
		return
	}
	data := map[string]any{}
	content, err := os.ReadFile(configPath)
	if err == nil && len(content) > 0 {
		_ = json.Unmarshal(content, &data)
	}
	if prev, ok := data["contacts_dir"].(string); ok && prev == contactsDir {
		return
	}
	data["contacts_dir"] = contactsDir
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(configPath, append(b, '\n'), 0o644)
}

func friendExistsByName(friends []Friend, name string) bool {
	target := normalizeFriendKey(name)
	if target == "" {
		return false
	}
	for _, f := range friends {
		if normalizeFriendKey(f.Name) == target {
			return true
		}
	}
	return false
}

func collectDashPrefixedArgs(args []string) ([]string, map[int]struct{}) {
	consumed := make(map[int]struct{})
	if len(args) == 0 {
		return nil, consumed
	}
	flags := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		token := args[i]
		if token == "--" {
			consumed[i] = struct{}{}
			break
		}
		if !strings.HasPrefix(token, "-") || token == "-" {
			continue
		}
		consumed[i] = struct{}{}
		keyToken := token
		if idx := strings.Index(token, "="); idx != -1 {
			keyToken = token[:idx]
		}
		key := normalizeFlagKey(keyToken)
		combined := token
		if !strings.Contains(token, "=") && flagRequiresValue(key) && i+1 < len(args) {
			next := args[i+1]
			if next != "--" && !strings.HasPrefix(next, "-") {
				consumed[i+1] = struct{}{}
				combined = fmt.Sprintf("%s=%s", token, next)
				i++
			}
		}
		flags = append(flags, combined)
	}
	return flags, consumed
}

func remainingArgs(args []string, consumed map[int]struct{}) []string {
	if len(args) == 0 {
		return nil
	}
	extra := make([]string, 0, len(args))
	for idx, token := range args {
		if _, ok := consumed[idx]; ok {
			continue
		}
		trimmed := strings.TrimSpace(token)
		if trimmed == "" {
			continue
		}
		extra = append(extra, trimmed)
	}
	return extra
}

func applyFlagTokens(tokens []string, opts *AppOptions, state *flagParseState) error {
	for _, token := range tokens {
		key, value, hasValue := splitFlagToken(token)
		switch key {
		case "v", "verbose":
			boolVal := true
			if hasValue && value != "" {
				parsed, err := strconv.ParseBool(value)
				if err != nil {
					return fmt.Errorf("invalid value for -v: %q", value)
				}
				boolVal = parsed
			}
			opts.Verbose = boolVal
		case "config":
			if !hasValue || value == "" {
				return fmt.Errorf("-config requires a value")
			}
			if opts.ConfigPath != "" && opts.ConfigPath != value {
				return fmt.Errorf("-config specified multiple times")
			}
			opts.ConfigPath = value
		case "contacts":
			if !hasValue || value == "" {
				return fmt.Errorf("-contacts requires a value")
			}
			opts.ContactsDir = value
			if state != nil {
				state.contactsOverride = true
			}
			if err := opts.setMode(ModeClient); err != nil {
				return err
			}
		case "port":
			if !hasValue || value == "" {
				return fmt.Errorf("-port requires a value")
			}
			p, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid port %q", value)
			}
			if p <= 0 || p > 65535 {
				return fmt.Errorf("port %d out of range", p)
			}
			opts.ListenPort = p
		default:
			if key == "" {
				return fmt.Errorf("unknown flag %q", token)
			}
			if _, ok := colorFilterSet[key]; ok {
				opts.ColorFilter = key
				continue
			}
			return fmt.Errorf("unknown flag %q", token)
		}
	}
	return nil
}

func splitFlagToken(token string) (string, string, bool) {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return "", "", false
	}
	parts := strings.SplitN(trimmed, "=", 2)
	key := normalizeFlagKey(parts[0])
	if len(parts) == 1 {
		return key, "", false
	}
	return key, parts[1], true
}

func normalizeFlagKey(raw string) string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimLeft(trimmed, "-")
	return strings.ToLower(trimmed)
}

func flagRequiresValue(key string) bool {
	switch key {
	case "config", "contacts", "port":
		return true
	default:
		return false
	}
}
