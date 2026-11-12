package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/svanichkin/say/lang"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/svanichkin/say/conf"
	"github.com/svanichkin/say/gui"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/network"
	tcpsignal "github.com/svanichkin/say/network/tcp"
	udptransport "github.com/svanichkin/say/network/udp"
	"github.com/svanichkin/say/ui"
	"golang.org/x/term"
)

var version = "dev"

const donateMonero = "Monero: 41uoDd1PNKm7j4LaBHHZ77ZPbEwEJzaRHhjEqFtKLZeWjd4sNfs3mtpbw1mcQrnNLBKWSJgui9ELEUz217Ui6kF13SmF4t5"
const support = "Say support: https://github.com/svanichkin/say"

func main() {
	lang.Init(os.Args[1:])
	crashLog := keepCrashes()
	run := run
	if chooseGUI(os.Args[1:]) {
		// The pixel window takes the main thread; Say runs beside it and
		// draws into it as into a terminal.
		ui.SetPixelMode()
		app := run
		run = func() error {
			return gui.Run(app, gui.Input{
				Key: ui.KeyInput, Paste: ui.PasteInput, Click: ui.ClickAt, Interrupt: ui.Interrupt,
				Resized: ui.RequestRedraw, Detail: ui.SetVideoDetail,
				Frame: ui.PixelFrame, FrameRGB: ui.FrameRGB,
				Accents: ui.Accents, Chat: ui.ChatAccent, Brick: ui.BrickColour, Mono: ui.AccentMono, Seeds: ui.FrameSeeds, Latency: ui.PeerLatencySnapshot,
			})
		}
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "[say] %v\n", err)
		showError(lang.T(lang.Stopped, err, crashLog))
		os.Exit(1)
	}
}

// keepCrashes sends what the Go runtime prints when Say crashes to a file
// too (say-crash.log in the configuration directory): started from
// Explorer with the window there is no console to show it. It returns the
// file's path.
func keepCrashes() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "say", "say-crash.log")
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return ""
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return ""
	}
	if debug.SetCrashOutput(f, debug.CrashOptions{}) != nil {
		f.Close()
		return ""
	}
	f.Close() // SetCrashOutput keeps its own handle
	return path
}

// chooseGUI decides between the window and the terminal before anything
// starts (the window has to be set up first; the arguments are parsed
// properly later). -gui and -text decide; with neither, where the window
// can be shown and Say runs in a terminal, it asks. The answer is added to
// the arguments, so that a relaunch in Windows Terminal does not ask again.
func chooseGUI(args []string) bool {
	switch {
	case flagGiven(args, "gui", "gui=true", "gui=1", "gui=on"):
		return true
	case flagGiven(args, "text", "gui=false", "gui=0", "gui=off"):
		return false
	case !gui.Available() || flagGiven(args, "version", "donate", "support", "help", "h"):
		return false
	case !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())):
		return false
	}
	window, asked := askModeDialog() // Windows, started from Explorer
	if !asked {
		fmt.Print(lang.T(lang.AskMode))
		window = askModeKey()
		if window {
			fmt.Println("GUI")
		} else {
			fmt.Println("TUI")
		}
	}
	if !window {
		os.Args = append(os.Args, "-text")
		return false
	}
	os.Args = append(os.Args, "-gui")
	hideConsole() // Windows: the console it was started in is not needed
	return true
}

// askModeKey takes the answer as a single key, without Enter: g (or Enter)
// for the window, t for the terminal; the same keys on a Russian layout
// (п, е) too. Other keys are ignored; Ctrl+C quits. Where the terminal
// cannot read single keys, a line is read instead.
func askModeKey() (window bool) {
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "t", "tui", "text", "т", "е":
			return false
		}
		return true
	}
	defer term.Restore(fd, old)
	buf := make([]byte, 8)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil || n == 0 {
			return true
		}
		r, _ := utf8.DecodeRune(buf[:n])
		switch unicode.ToLower(r) {
		case 'g', 'п', '\r', '\n':
			return true
		case 't', 'е', 'т':
			return false
		case 3: // Ctrl+C
			term.Restore(fd, old)
			fmt.Println()
			os.Exit(130)
		}
	}
}

// flagGiven reports whether one of the flags (without dashes) is among the
// arguments.
func flagGiven(args []string, names ...string) bool {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		a = strings.ToLower(strings.TrimLeft(a, "-"))
		for _, n := range names {
			if a == n {
				return true
			}
		}
	}
	return false
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
	if !opts.NoWT && !opts.GUI && relaunchInWindowsTerminal() {
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
	log.SetOutput(logs.Tap(logOutput)) // DEBUG-TEMP: Tap keeps lines for the peer
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if logWriter != nil && logErr == nil { // DEBUG-TEMP: the peer's log, beside ours
		logs.SetPeerLogPath(strings.TrimSuffix(logPath, ".log") + "-peer.log")
		log.Printf("[debug] %s %s, boot %s, %s/%s, args %q", role, appVersion(), logs.BootID, runtime.GOOS, runtime.GOARCH, os.Args[1:])
	}
	if logWriter != nil && logErr == nil {
		fmt.Fprintf(os.Stderr, "[say] logs: %s\n", logPath)
	} else if logErr != nil {
		fmt.Fprintf(os.Stderr, "[say] log file disabled (%v)\n", logErr)
	}

	appCtx, appCancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer appCancel()
	ui.SetInterrupt(appCancel) // Ctrl+C and the window's close button: no signal needed (Windows has none)

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

	initialStatus := lang.T(lang.BootServer)
	if opts.Mode == conf.ModeClient {
		initialStatus = lang.T(lang.BootClient)
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
		log.SetOutput(logs.Tap(logWriter)) // DEBUG-TEMP: Tap keeps lines for the peer
	} else {
		log.SetOutput(logs.Tap(io.Discard))
	}
	ui.SetStatusMessage(initialStatus)
	defer func() {
		if err != nil {
			ui.SetStatusMessage(lang.T(lang.Failure) + "\n" + err.Error())
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
		ui.SetStatusMessage(lang.T(lang.JackIn) + "\n" + lang.T(lang.YggMesh))
	} else {
		ui.SetStatusMessage(lang.T(lang.JackIn) + "\n" + lang.T(lang.RawLink))
	}
	if err := transport.WaitReady(appCtx, 30*time.Second); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	localAddr := strings.TrimSpace(transport.LocalAddr())
	ui.SetAccentAddresses(localAddr, "") // the window's colour for us
	if localAddr != "" {
		logs.LogV("[p2p] online %s", network.PrettyAddr(localAddr, opts.ListenPort))
	} else {
		logs.LogV("[p2p] online (%s)", opts.Transport)
	}
	if opts.DialAddr == "" {
		if localAddr != "" {
			hint := formatSayHint(opts.Transport, localAddr, opts.ListenPort)
			ui.SetCopyableStatus(lang.T(lang.NodeOnline)+"\n"+hint+"\n"+lang.T(lang.HandOver), hint)
		} else if opts.Transport == network.TransportPlain {
			ui.SetStatusMessage(lang.T(lang.NodeOnline) + "\n" + lang.T(lang.RawLink) + "\n" + lang.T(lang.Listening, opts.ListenPort))
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
			ui.SetAccentAddresses(localAddr, peerHost)
			ui.ResetDetailBoost() // a new call earns the finer detail afresh
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
			go logCallHealth(peerHost, sess.Done()) // DEBUG-TEMP
			go showPeerPath(transport, peerHost, sess.Done())
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
			ui.SetStatusMessage(lang.T(lang.Dialing) + "\n" + target + "\n" + lang.T(lang.Punching))
		} else {
			ui.SetStatusMessage(lang.T(lang.Redial) + "\n" + target + "\n" + lang.T(lang.Attempt, attempt))
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
		dialStarted := time.Now() // DEBUG-TEMP
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
			ui.SetStatusMessage(lang.T(lang.Uplink) + "\n" + target + "\n" + lang.T(lang.Handshake))
			log.Printf("[p2p] dial established to %s", target)
			log.Printf("[debug] redial: attempt %d connected in %v", attempt, time.Since(dialStarted).Round(time.Millisecond)) // DEBUG-TEMP
			established := time.Now()                                                                                          // DEBUG-TEMP
			delay = initialDelay
			attempt = 1

			select {
			case <-ctx.Done():
				stopClient()
				return ctx.Err()
			case <-connDone:
				stopClient()
				log.Printf("[p2p] connection to %s lost", target)
				// DEBUG-TEMP: why is in the "control stream ... ended" line just before.
				log.Printf("[debug] redial: the call lasted %v; dialing again now", time.Since(established).Round(time.Millisecond))
				ui.SetStatusMessage(lang.T(lang.SignalLost) + "\n" + target + "\n" + lang.T(lang.Rerouting))
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
			ui.SetStatusMessage(lang.T(lang.LineBusy) + "\n" + target + "\n" + lang.T(lang.BusyRetry, int(delay.Seconds())))
		} else {
			log.Printf("[p2p] dial failed: %v", err)
			log.Printf("[debug] redial: attempt %d failed after %v; next in %v", attempt, time.Since(dialStarted).Round(time.Millisecond), delay) // DEBUG-TEMP
			ui.SetStatusMessage(lang.T(lang.NoCarrier) + "\n" + target + "\n" + err.Error() + "\n" + lang.T(lang.Retry, int(delay.Seconds())))
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

// showPeerPath shows next to the ping which way the call goes, P2P or
// through Yggdrasil, until the media session ends. A direct link comes and
// goes during a call, so it is looked at every second.
func showPeerPath(transport network.Transport, peer string, done <-chan struct{}) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	last := ""
	for {
		path := ui.PathMesh
		if transport.IsDirectPeer(peer) {
			path = ui.PathDirect
		}
		if path != last {
			log.Printf("[p2p] the call goes %s to %s", path, peer)
			last = path
		}
		ui.UpdatePeerPath(path)
		select {
		case <-done:
			ui.UpdatePeerPath("")
			return
		case <-tick.C:
		}
	}
}

// directSession is a call's media session together with its direct link.
// logCallHealth logs the call's state every few seconds until the media
// session ends, and how long it lasted. DEBUG-TEMP: remove once calls are
// reliable.
func logCallHealth(peer string, done <-chan struct{}) {
	started := time.Now()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-done:
			log.Printf("[debug] media with %s stopped after %v", peer, time.Since(started).Round(time.Second))
			return
		case <-tick.C:
			txA, txV, rxA, rxV := ui.NetRateSnapshot()
			rxVL, rxVAt := ui.RxVideoLossSnapshot()
			peerVL, peerVAt := ui.PeerVideoLossSnapshot()
			rxAL, rxAAt := ui.RxAudioLossSnapshot()
			age := func(t time.Time) string {
				if t.IsZero() {
					return "never"
				}
				return time.Since(t).Round(time.Second).String() + " ago"
			}
			log.Printf("[debug] health %s +%v: rtt %v; tx audio %.1f video %.1f kB/s; rx audio %.1f video %.1f kB/s; rx video loss %.0f%% (%s), peer's of ours %.0f%% (%s), rx audio loss %.0f%% (%s); ygg %v",
				peer, time.Since(started).Round(time.Second), ui.PeerLatencySnapshot(), txA, txV, rxA, rxV,
				rxVL*100, age(rxVAt), peerVL*100, age(peerVAt), rxAL*100, age(rxAAt), network.Connected())
		}
	}
}

type directSession struct {
	tcpsignal.MediaSession
	stopDirect func()
}

func (s *directSession) Stop() error {
	s.stopDirect()
	return s.MediaSession.Stop()
}
