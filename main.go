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
	if !opts.NoWT && relaunchInWindowsTerminal() {
		return nil // Say goes on in a Windows Terminal window
	}

	udptransport.SetMaxVideoFPS(opts.MaxVideoFPS)
	if err := udptransport.SetAudioCodec(opts.AudioCodec); err != nil {
		return err
	}
	if opts.Camera != "" {
		// gocam opens this device only (Linux; e.g. a v4l2loopback node).
		os.Setenv("GOCAM_DEVICE", opts.Camera)
	}
	// The handshake tells the peer our version and the codec we send.
	tcpsignal.SetLocalInfo(appVersion(), opts.AudioCodec)
	ui.SetVideoFPSLimit(opts.MaxVideoFPS)

	role := "server"
	if opts.Mode == conf.ModeClient {
		role = "client"
	}
	logWriter, closeLog, logPath, logErr := initLogSink(opts.ConfigPath, role, opts.LogEnabled)
	if closeLog != nil {
		defer closeLog()
	}
	logOutput := io.Writer(os.Stderr)
	if logWriter != nil {
		logOutput = io.MultiWriter(os.Stderr, logWriter)
	}
	log.SetOutput(logOutput)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if logWriter != nil && logErr == nil {
		fmt.Fprintf(os.Stderr, "[say] logs: %s\n", logPath)
	} else if logErr != nil {
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
		Current: func() (tcpsignal.TermSize, bool) {
			vs, ok := ui.CurrentViewport()
			return tcpsignal.TermSize{Cols: vs.Cols, Rows: vs.Rows}, ok
		},
		OnPeer: func(cols, rows int) {
			ui.SetPeerTermSize(cols, rows)
		},
	}

	initialStatus := "Server starting…"
	if opts.Mode == conf.ModeClient {
		initialStatus = "Client starting…"
	}

	udptransport.Configure(true, true)
	udptransport.SetMicrophone(!opts.NoMic)
	udptransport.SetSystemVoice(opts.SysAudio)
	udptransport.SetVideoShapeHold(opts.VideoHold)
	udptransport.SetVideoHoldDebug(opts.HoldDebug)
	network.SetLAN(!opts.NoLAN)
	network.SetDirect(!opts.NoDirect)
	ui.SetColorFilter(opts.ColorFilter)
	tcpsignal.SetLocalMonoView(ui.ColorFilterActive())
	ui.SetColorMode(opts.Colors)
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

	transport, err := network.SetupTransport(opts.Transport, conf.Verbose, opts.ConfigPath)
	if err != nil {
		return err
	}
	defer transport.Close()
	if transport.Kind() == network.TransportYggdrasil {
		ui.SetStatusMessage("Connecting to Ygg network…")
	} else {
		ui.SetStatusMessage("Preparing TCP/UDP transport…")
	}
	if err := transport.WaitReady(appCtx, 30*time.Second); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	localAddr := strings.TrimSpace(transport.LocalAddr())
	if localAddr != "" {
		logs.LogV("[p2p] online %s", network.PrettyAddr(localAddr, opts.ListenPort))
	} else {
		logs.LogV("[p2p] online (%s)", opts.Transport)
	}
	if opts.DialAddr == "" {
		if localAddr != "" {
			hint := formatSayHint(opts.Transport, localAddr, opts.ListenPort)
			ui.SetCopyableStatus(hint, hint)
		} else if opts.Transport == network.TransportPlain {
			ui.SetStatusMessage(fmt.Sprintf("Plain TCP/UDP transport ready on port %d", opts.ListenPort))
		}
	}

	udpConn, err := transport.ListenUDP(opts.ListenPort)
	if err != nil {
		return fmt.Errorf("udp listen failed: %w", err)
	}
	defer udpConn.Close()
	if localAddr != "" {
		logs.LogV("[p2p] UDP bound on %s", network.PrettyAddr(localAddr, opts.ListenPort))
	} else if udpAddr := udpConn.LocalAddr(); udpAddr != nil {
		logs.LogV("[p2p] UDP bound on %s", udpAddr.String())
	}

	var mediaFactory tcpsignal.MediaSessionFactory
	if udpConn != nil {
		mediaFactory = func(peerHost string, remote *net.UDPAddr, peer tcpsignal.PeerInfo) (tcpsignal.MediaSession, error) {
			// Every call starts with the default audio buffers (-audio
			// changes last one call). Where the peer is only matters for
			// the log: a LAN peer means multicast found a direct link.
			udptransport.ResetAudioSettings()
			if transport.IsLocalPeer(peerHost) {
				log.Printf("[p2p] peer %s is in the local network (direct link)", peerHost)
			} else {
				log.Printf("[p2p] peer %s is reached over the internet", peerHost)
			}
			// We decode the peer's voice with the codec it announced (an
			// older peer announces none and expects ours).
			sess, err := udptransport.StartSession(udpConn, remote, peer.AudioCodec, peer.Proto, peer.MonoView)
			if err != nil {
				return nil, err
			}
			if remote == nil {
				return sess, nil
			}
			// The caller asks for a direct link (unless -nodirect on either
			// side); it lasts as long as the call.
			return &directSession{MediaSession: sess, stopDirect: transport.StartDirect(peerHost)}, nil
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
		tcp, err := transport.ListenTCP(opts.ListenPort)
		if err != nil {
			return fmt.Errorf("listen failed: %w", err)
		}
		stopServer, err := tcpsignal.StartSignalServerTCP(tcp, friends, opts.ListenPort, termSync, mediaFactory)
		if err != nil {
			return fmt.Errorf("listen failed: %w", err)
		}
		defer stopServer()
		if localAddr != "" {
			log.Printf("[hint] %s", formatSayHint(opts.Transport, localAddr, opts.ListenPort))
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
			autodialErrCh <- autodial(dialCtx, host, port, termSync, mediaFactory, transport.DialTCP)
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

// initLogSink opens the log file. role ("client" or "server") is part of the
// file name, so a client and a server started from the same binary and
// config do not write into one file.
func initLogSink(configPath, role string, logEnabled bool) (io.Writer, func() error, string, error) {
	if !logEnabled {
		return nil, nil, "", nil
	}
	logPath, err := resolveLogPath(configPath, role, logEnabled)
	if err != nil {
		return nil, nil, "", err
	}
	dir := filepath.Dir(logPath)
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, "", err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, logPath, err
	}
	closeFn := func() error {
		return f.Close()
	}
	return f, closeFn, logPath, nil
}

func resolveLogPath(configPath, role string, logEnabled bool) (string, error) {
	if logEnabled {
		exePath, err := os.Executable()
		if err != nil {
			return "", err
		}
		if resolvedExePath, err := filepath.EvalSymlinks(exePath); err == nil && resolvedExePath != "" {
			exePath = resolvedExePath
		}
		name := strings.TrimSpace(filepath.Base(configPath))
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = "say"
		}
		ext := filepath.Ext(name)
		if ext != "" {
			name = strings.TrimSuffix(name, ext)
		}
		if name == "" {
			name = "say"
		}
		return filepath.Join(filepath.Dir(exePath), name+"-"+role+".log"), nil
	}

	dir := filepath.Dir(configPath)
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, "say-"+role+".log"), nil
}

func formatSayHint(transport network.TransportKind, addr string, port int) string {
	target := formatPeerAddr(addr, port)
	if transport == network.TransportPlain {
		return fmt.Sprintf("say -transport plain \"%s\"", target)
	}
	return fmt.Sprintf("say \"%s\"", target)
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

func autodial(ctx context.Context, host string, port int, termSync *tcpsignal.TermSizeSync, mediaFactory tcpsignal.MediaSessionFactory, dial tcpsignal.TCPDialFunc) error {
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

		// The dial and the handshake have their own timeouts (up to 10 s
		// each) and take no context: run them aside so Ctrl-C does not wait
		// for them. A connection that still comes up afterwards is hung up.
		type dialResult struct {
			stop func() error
			done <-chan struct{}
			err  error
		}
		dialed := make(chan dialResult, 1)
		go func() {
			stop, done, err := tcpsignal.StartSignalClientTCP(conf.HostnameOr("me"), host, port, termSync, mediaFactory, dial)
			dialed <- dialResult{stop, done, err}
		}()
		var res dialResult
		select {
		case <-ctx.Done():
			go func() {
				if r := <-dialed; r.err == nil {
					r.stop()
				}
			}()
			return ctx.Err()
		case res = <-dialed:
		}
		stopClient, connDone, err := res.stop, res.done, res.err
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

		var incompatible *tcpsignal.IncompatibleError
		if errors.As(err, &incompatible) {
			// Redialing cannot help until one side is updated.
			log.Printf("[p2p] %s: %v", target, err)
			ui.SetStatusMessage(err.Error())
			return err
		}
		if errors.Is(err, tcpsignal.ErrPeerBusy) {
			log.Printf("[p2p] %s is busy with another call", target)
			ui.SetStatusMessage(fmt.Sprintf("%s is busy with another call\nRetrying in %ds…", target, int(delay.Seconds())))
		} else {
			log.Printf("[p2p] dial failed: %v", err)
			ui.SetStatusMessage(fmt.Sprintf("Dial failed: %v\nRetrying in %ds…", err, int(delay.Seconds())))
		}

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

// directSession is a call's media session together with its direct link.
type directSession struct {
	tcpsignal.MediaSession
	stopDirect func()
}

func (s *directSession) Stop() error {
	s.stopDirect()
	return s.MediaSession.Stop()
}
