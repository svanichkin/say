package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/svanichkin/say/conf"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/network"
	tcpsignal "github.com/svanichkin/say/network/tcp"
	udptransport "github.com/svanichkin/say/network/udp"
	"github.com/svanichkin/say/ui"

	ygg "github.com/svanichkin/ygg"
)

var version = "dev"

const donateMonero = "Monero: 41uoDd1PNKm7j4LaBHHZ77ZPbEwEJzaRHhjEqFtKLZeWjd4sNfs3mtpbw1mcQrnNLBKWSJgui9ELEUz217Ui6kF13SmF4t5"
const support = "Say support: https://github.com/svanichkin/say"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "[say] %v\n", err)
		os.Exit(1)
	}
}

func run() (err error) {
	opts, err := conf.ParseCLI()
	if err != nil {
		return err
	}
	if opts.ShowVersion {
		printVersion()
		if !opts.ShowDonate {
			return nil
		}
	}
	if opts.ShowDonate {
		printDonateInfo()
		return nil
	}
	if opts.Support {
		printSupportInfo()
		return nil
	}

	udptransport.SetMaxVideoFPS(opts.MaxVideoFPS)
	ui.SetVideoFPSLimit(opts.MaxVideoFPS)

	logWriter, closeLog, logPath, logErr := initLogSink(opts.ConfigPath)
	if closeLog != nil {
		defer closeLog()
	}
	logOutput := io.Writer(os.Stderr)
	if logWriter != nil {
		logOutput = io.MultiWriter(os.Stderr, logWriter)
	}
	log.SetOutput(logOutput)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if logErr == nil {
		fmt.Fprintf(os.Stderr, "[say] logs: %s\n", logPath)
	} else {
		fmt.Fprintf(os.Stderr, "[say] log file disabled (%v)\n", logErr)
	}

	appCtx, appCancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer appCancel()

	viewportFeed := ui.NewViewportFeed(appCtx)
	defer viewportFeed.Stop()
	termUpdates := make(chan tcpsignal.TermSize, 4)
	go func() {
		defer close(termUpdates)
		for ts := range viewportFeed.Updates {
			termUpdates <- tcpsignal.TermSize{Cols: ts.Cols, Rows: ts.Rows}
		}
	}()
	var initialTerm *tcpsignal.TermSize
	if viewportFeed.Initial != nil {
		initial := tcpsignal.TermSize{Cols: viewportFeed.Initial.Cols, Rows: viewportFeed.Initial.Rows}
		initialTerm = &initial
	}
	termSync := &tcpsignal.TermSizeSync{
		Local:   termUpdates,
		Initial: initialTerm,
		OnPeer: func(cols, rows int) {
			ui.SetPeerTermSize(cols, rows)
		},
	}

	initialStatus := "Server starting…"
	if opts.Mode == conf.ModeClient {
		initialStatus = "Client starting…"
	}

	udptransport.Configure(true, true)
	ui.SetColorFilter(opts.ColorFilter)
	ui.EnsureRenderer(appCtx)
	if logWriter != nil {
		log.SetOutput(logWriter)
	} else {
		log.SetOutput(io.Discard)
	}
	ui.SetStatusMessage(initialStatus)
	defer func() {
		if err != nil {
			ui.SetStatusMessage("Error: " + err.Error())
		} else {
			ui.SetStatusMessage("")
		}
	}()

	// Initialize the embedded Yggdrasil node with the configured verbosity, peer limits and callbacks.
	var yggAddrStr string
	connectedCh := make(chan struct{}, 1)
	node, yggAddrStr, err := network.SetupYgg(conf.Verbose, opts.ConfigPath, func(connected bool) {
		if connected {
			logs.LogV("[p2p] online %s", network.PrettyAddr(yggAddrStr, opts.ListenPort))
			select {
			case connectedCh <- struct{}{}:
			default:
			}
		} else {
			logs.LogV("[p2p] offline %s", network.PrettyAddr(yggAddrStr, opts.ListenPort))
		}
	})
	if err != nil {
		return err
	}
	defer node.Close()
	ui.SetStatusMessage("Connecting to Ygg network…")
	if err := waitForConnectivity(appCtx, connectedCh, 30*time.Second); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	if opts.DialAddr == "" {
		hint := formatSayHint(yggAddrStr, opts.ListenPort)
		ui.SetCopyableStatus(hint, hint)
	}

	udpConn, err := ygg.ListenUDP(opts.ListenPort)
	if err != nil {
		return fmt.Errorf("udp listen failed: %w", err)
	}
	defer udpConn.Close()
	logs.LogV("[p2p] UDP bound on %s", network.PrettyAddr(yggAddrStr, opts.ListenPort))

	var mediaFactory tcpsignal.MediaSessionFactory
	if udpConn != nil {
		mediaFactory = func(remote *net.UDPAddr) (tcpsignal.MediaSession, error) {
			return udptransport.StartSession(udpConn, remote)
		}
	}

	_, contactsFromCfg, loadErr := conf.LoadFriendsFromConfig(opts.ConfigPath)
	if loadErr != nil {
		log.Printf("[cfg] couldn't read friends from %s: %v", opts.ConfigPath, loadErr)
	}
	friends := make([]conf.Friend, 0)
	contactsDir := opts.ContactsDir
	if contactsDir == "" {
		contactsDir = contactsFromCfg
	}
	if contactsDir != "" {
		if contactFriends, err := conf.LoadFriendsFromContacts(contactsDir); err != nil {
			log.Printf("[contacts] %v", err)
		} else if len(contactFriends) > 0 {
			logs.LogV("[contacts] %d entries from %s", len(contactFriends), contactsDir)
			friends = mergeFriendLists(friends, contactFriends)
		}
	}
	if opts.FriendName != "" {
		if opts.Mode != conf.ModeClient {
			log.Printf("[contacts] friend %q ignored in listen mode", opts.FriendName)
		} else {
			addr := lookupFriendAddress(friends, opts.FriendName)
			if addr == "" {
				return fmt.Errorf("friend %q not found in contacts", opts.FriendName)
			}
			if opts.DialAddr == "" {
				opts.DialAddr = addr
			}
		}
	}

	var (
		cancelAutodial context.CancelFunc
		autodialErrCh  chan error
	)

	if opts.DialAddr == "" {
		tcp, err := ygg.ListenTCP(opts.ListenPort)
		if err != nil {
			return fmt.Errorf("listen failed: %w", err)
		}
		stopServer, err := tcpsignal.StartSignalServerTCP(tcp, friends, opts.ListenPort, termSync, mediaFactory)
		if err != nil {
			return fmt.Errorf("listen failed: %w", err)
		}
		defer stopServer()
		if yggAddrStr != "" {
			log.Printf("[hint] %s", formatSayHint(yggAddrStr, opts.ListenPort))
		}
	} else {
		host, port, err := conf.ParseDialTarget(opts.DialAddr, opts.ListenPort)
		if err != nil {
			return fmt.Errorf("dial target: %w", err)
		}
		autodialErrCh = make(chan error, 1)
		dialCtx, cancel := context.WithCancel(appCtx)
		cancelAutodial = cancel
		go func() {
			autodialErrCh <- autodial(dialCtx, host, port, termSync, mediaFactory)
		}()
	}

	select {
	case <-appCtx.Done():
		log.Println("shutting down")
		if cancelAutodial != nil {
			cancelAutodial()
			if err := <-autodialErrCh; err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		}
		return nil
	case err := <-autodialErrCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}
}

func lookupFriendAddress(friends []conf.Friend, name string) string {
	for _, f := range friends {
		if strings.EqualFold(f.Name, name) {
			return strings.TrimSpace(f.Address)
		}
	}
	return ""
}

func mergeFriendLists(base, extra []conf.Friend) []conf.Friend {
	if len(extra) == 0 {
		return base
	}
	merged := make([]conf.Friend, 0, len(base)+len(extra))
	seen := make(map[string]struct{}, len(base)+len(extra))
	appendUnique := func(list []conf.Friend) {
		for _, f := range list {
			key := strings.ToLower(strings.TrimSpace(f.Name))
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, f)
		}
	}
	appendUnique(base)
	appendUnique(extra)
	return merged
}

func initLogSink(configPath string) (io.Writer, func() error, string, error) {
	dir := filepath.Dir(configPath)
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, "", err
	}
	logPath := filepath.Join(dir, "say.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, logPath, err
	}
	closeFn := func() error {
		return f.Close()
	}
	return f, closeFn, logPath, nil
}

func formatSayHint(addr string, port int) string {
	host := normalizeHost(addr)
	if host == "" {
		host = "<address>"
	}
	if port > 0 && port != network.DefaultListenPort {
		if strings.Contains(host, ":") {
			host = fmt.Sprintf("[%s]", host)
		}
		host = fmt.Sprintf("%s:%d", host, port)
	}
	return fmt.Sprintf("say \"%s\"", host)
}

func formatPeerAddr(host string, port int) string {
	plain := normalizeHost(host)
	if plain == "" {
		return "<address>"
	}
	if port > 0 && port != network.DefaultListenPort {
		if strings.Contains(plain, ":") {
			return fmt.Sprintf("[%s]:%d", plain, port)
		}
		return fmt.Sprintf("%s:%d", plain, port)
	}
	return plain
}

func normalizeHost(addr string) string {
	host := strings.TrimSpace(addr)
	host = strings.Trim(host, "[]")
	return host
}

func autodial(ctx context.Context, host string, port int, termSync *tcpsignal.TermSizeSync, mediaFactory tcpsignal.MediaSessionFactory) error {
	const (
		initialDelay = time.Second
		maxDelay     = 15 * time.Second
	)

	target := formatPeerAddr(host, port)
	delay := initialDelay
	attempt := 1

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if attempt == 1 {
			ui.SetStatusMessage(fmt.Sprintf("Dialing %s…", target))
		} else {
			ui.SetStatusMessage(fmt.Sprintf("Re-dialing %s (attempt %d)…", target, attempt))
		}

		stopClient, connDone, err := tcpsignal.StartSignalClientTCP(conf.HostnameOr("me"), host, port, termSync, mediaFactory)
		if err == nil {
			ui.SetStatusMessage(fmt.Sprintf("Connected to %s", target))
			log.Printf("[p2p] dial established to %s", target)
			delay = initialDelay
			attempt = 1

			select {
			case <-ctx.Done():
				stopClient()
				return ctx.Err()
			case <-connDone:
				stopClient()
				log.Printf("[p2p] connection to %s lost", target)
				ui.SetStatusMessage(fmt.Sprintf("Connection lost. Re-dialing %s…", target))
				continue
			}
		}

		log.Printf("[p2p] dial failed: %v", err)
		ui.SetStatusMessage(fmt.Sprintf("Dial failed: %v\nRetrying in %ds…", err, int(delay.Seconds())))

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}

		if delay < maxDelay {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
		attempt++
	}
}

func waitForConnectivity(ctx context.Context, ch <-chan struct{}, timeout time.Duration) error {
	select {
	case <-ch:
		return nil
	default:
	}

	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ch:
		return nil
	case <-timer.C:
		return fmt.Errorf("ygg connectivity timeout after %s", timeout)
	}
}

func appVersion() string {
	v := strings.TrimSpace(version)
	if v == "" {
		v = "dev"
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v == "dev" {
			if ver := strings.TrimSpace(bi.Main.Version); ver != "" && ver != "(devel)" {
				return ver
			}
		}
		if v == "dev" {
			if derived := vcsVersion(bi); derived != "" {
				return derived
			}
		}
	}
	return v
}

func vcsVersion(bi *debug.BuildInfo) string {
	revision := buildInfoSetting(bi, "vcs.revision")
	if revision == "" {
		return ""
	}
	short := revision
	if len(short) > 12 {
		short = short[:12]
	}
	dirty := ""
	if buildInfoSetting(bi, "vcs.modified") == "true" {
		dirty = "+dirty"
	}
	if ts := buildInfoSetting(bi, "vcs.time"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			return fmt.Sprintf("v0.0.0-%s-%s%s", t.UTC().Format("20060102150405"), short, dirty)
		}
	}
	return short + dirty
}

func buildInfoSetting(bi *debug.BuildInfo, key string) string {
	for _, setting := range bi.Settings {
		if setting.Key == key {
			return setting.Value
		}
	}
	return ""
}

func printVersion() {
	fmt.Printf("say %s\n", appVersion())
}

func printDonateInfo() {
	fmt.Println("Donate:")
	fmt.Println(donateMonero)
}

func printSupportInfo() {
	fmt.Println(support)
}
