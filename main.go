package main

import (
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
	"sync"
	"syscall"
	"time"

	"github.com/svanichkin/say/conf"
	"github.com/svanichkin/say/contacts"
	"github.com/svanichkin/say/dbg"
	"github.com/svanichkin/say/device"
	"github.com/svanichkin/say/gui"
	"github.com/svanichkin/say/logs"
	"github.com/svanichkin/say/network"
	tcpsignal "github.com/svanichkin/say/network/tcp"
	udptransport "github.com/svanichkin/say/network/udp"
	"github.com/svanichkin/say/tones"
	"github.com/svanichkin/say/ui"
	"golang.org/x/term"
)

var version = "dev"

const donateMonero = "Monero: 41uoDd1PNKm7j4LaBHHZ77ZPbEwEJzaRHhjEqFtKLZeWjd4sNfs3mtpbw1mcQrnNLBKWSJgui9ELEUz217Ui6kF13SmF4t5"
const support = "Say support: https://github.com/svanichkin/say"

func main() {
	os.Args = dropProcessSerial(os.Args)
	lang.Init(os.Args[1:])
	crashLog := keepCrashes()
	run := run
	if chooseGUI(os.Args[1:]) {
		// The window takes the main thread; Say runs beside it and draws
		// into it as into a terminal.
		ui.SetPixelMode()
		app := run
		run = func() error {
			return gui.Run(app, gui.Input{
				Key: ui.KeyInput, Paste: ui.PasteInput, Click: ui.ClickAt, Hover: ui.HoverAt, Interrupt: ui.Interrupt,
				Resized: ui.RequestRedraw, Detail: ui.StepVideoDetail,
				Frame: ui.PixelFrame, FrameRGB: ui.FrameRGB, FrameRGBRow: ui.FrameRGBRow, FrameY: ui.FrameY,
				Accents: ui.Accents, Chat: ui.ChatAccent, Brick: ui.BrickColour, Mono: ui.AccentMono, Seeds: ui.FrameSeeds, Latency: ui.PeerLatencySnapshot,
				Place: windowPlacePath(os.Args[1:]),
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
// properly later). -gui and -text decide. With neither: started in a
// terminal, Say works in it; started as a program (a double click in
// Explorer, which gives Say a console of its own; an app bundle or a
// launcher, with no terminal at all), it opens its window where this build
// has one. Over SSH always the terminal (a window would open on the remote
// machine's screen, if at all). The choice is added to the arguments, so that
// what parses them later knows it.
func chooseGUI(args []string) bool {
	switch {
	case flagGiven(args, "gui", "gui=true", "gui=1", "gui=on"):
		return true
	case flagGiven(args, "text", "gui=false", "gui=0", "gui=off"):
		return false
	case !gui.Available() || overSSH() || flagGiven(args, "version", "donate", "support", "help", "h"):
		return false
	case !startedAsProgram():
		return false
	}
	os.Args = append(os.Args, "-gui")
	hideConsole() // Windows: the console Explorer opened for Say is not needed
	return true
}

// startedAsProgram reports whether Say was started as a program rather than
// as the terminal utility. On macOS the program is Say.app: started from the
// bundle; the bare say is the utility, in a terminal however it is started
// (a double click on it opens Terminal anyway). On Windows a double click in
// Explorer (a console of its own); typed in cmd, PowerShell or Windows
// Terminal it is the utility, its output redirected or not. On Plan 9 a rio
// window of its own, not vt. Elsewhere there is no window yet: no terminal
// at all.
var startedAsProgram = func() bool {
	switch runtime.GOOS {
	case "windows":
		return doubleClicked()
	case "plan9":
		// In vt (it sets $TERM) Say is the terminal utility; in a rio
		// window of its own (or drawterm's) it draws the window.
		return os.Getenv("TERM") == ""
	case "darwin":
		exe, err := os.Executable()
		if err != nil {
			return false
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		return inAppBundle(exe)
	}
	return !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd()))
}

// inAppBundle reports whether the executable at path is a macOS app
// bundle's (…/Say.app/Contents/MacOS/say).
func inAppBundle(path string) bool {
	dir := filepath.Dir(path)
	return filepath.Base(dir) == "MacOS" && filepath.Base(filepath.Dir(dir)) == "Contents" &&
		strings.HasSuffix(filepath.Dir(filepath.Dir(dir)), ".app")
}

// windowPlacePath is the file that keeps the window's place, beside the
// configuration: config.json keeps it in config-window.json.
func windowPlacePath(args []string) string {
	cfg := conf.ConfigPathFromArgs(args)
	if cfg == "" {
		return ""
	}
	return strings.TrimSuffix(cfg, filepath.Ext(cfg)) + "-window.json"
}

// dropProcessSerial drops the -psn_… argument that macOS adds when Finder
// starts an app bundle (older versions do): it is not Say's, and an unknown
// flag would stop it.
func dropProcessSerial(args []string) []string {
	out := args[:0:0]
	for _, a := range args {
		if !strings.HasPrefix(a, "-psn_") {
			out = append(out, a)
		}
	}
	return out
}

// overSSH reports whether Say was started in an SSH session.
func overSSH() bool {
	return os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_CLIENT") != "" || os.Getenv("SSH_TTY") != ""
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
	if !opts.NoWT && !opts.GUI && relaunchInTerminal() {
		return nil // Say goes on in a terminal window of its own
	}
	// One configuration (one Yggdrasil address) runs once: two copies with
	// it reset each other's calls.
	releaseConfig, err := conf.LockConfig(opts.ConfigPath)
	if err != nil {
		return err
	}
	defer releaseConfig()

	udptransport.SetMaxVideoFPS(opts.MaxVideoFPS)
	if err := udptransport.SetAudioCodec(opts.AudioCodec); err != nil {
		return err
	}
	switch {
	case device.IsCameraFile(opts.Camera):
		device.SetCameraFile(opts.Camera) // a clip as the camera
	case opts.Camera != "":
		// gocam opens this device only (Linux; e.g. a v4l2loopback node).
		os.Setenv("GOCAM_DEVICE", opts.Camera)
	}
	// The handshake tells the peer our version and the codec we send.
	tcpsignal.SetLocalInfo(appVersion(), opts.AudioCodec)
	ui.SetVideoFPSLimit(opts.MaxVideoFPS)
	ui.SetVersion(appVersion())

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
	if dbg.On && logWriter != nil && logErr == nil { // DEBUG-TEMP: the peer's log, beside ours
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
	udptransport.SetVideoBandRows(opts.VideoBand)
	if err := udptransport.SetVideoCodec(opts.VideoCodec, opts.VideoQ, opts.VideoQMax); err != nil {
		return err
	}
	// Smooth video in the window: the peer is asked for pixels, not cells;
	// and told when it asks the same of us.
	tcpsignal.SetPixelVideo(opts.VideoCodec != "say" && ui.PixelMode())
	tcpsignal.SetOnPeerPixels(udptransport.SetPeerPixels)
	if err := gui.SetVideoEffect(opts.VideoFX); err != nil {
		return err
	}
	gui.SetVideoLight(opts.VideoLight)
	gui.SetVideoSharp(opts.VideoSharp)
	gui.SetVideoDelay(ui.VideoLagSnapshot)
	if err := udptransport.SetNetSim(opts.NetSim); err != nil {
		return err
	}
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
	device.WarmTones(tones.Rate) // a call's ring starts at once
	defer func() {
		if err != nil {
			ui.ShowHome(false) // the reason, not the menu
			ui.SetStatusMessage(lang.T(lang.Failure) + "\n" + err.Error())
		} else {
			ui.SetStatusMessage("")
		}
	}()

	// The address book: the home screen's, and callers' names. Waiting for
	// calls, the home screen shows at once, while the node comes up (our
	// address on it once it is known; a call made before it is up waits).
	// A call by an address given on the command line goes there first; the
	// home screen comes after it.
	homeSide := newHomeApp(opts.ConfigPath, opts.ContactsDir, "", opts.ListenPort)
	ui.SetHangup(homeSide.hangup)
	// The window's close button hides it (gui), and so does x on the first
	// column: Say goes on, waiting for calls; a call coming in shows it
	// again. Cmd+Q, Ctrl+C quit; in a terminal (and Plan 9's window) x does.
	quit := appCancel
	if ui.PixelMode() {
		ui.SetShowWindow(gui.ShowWindow)
		if runtime.GOOS != "plan9" {
			quit = gui.HideWindow
		}
	}
	ui.SetHome(homeSide.root, homeSide.paste, quit)
	homeSide.initTray(trayPath(os.Args[1:]), appCancel) // Say's icon in the Dock or the tray (tray.go)
	if opts.Mode != conf.ModeClient {
		ui.ShowHome(true)
	}

	transport, err := network.SetupTransport(opts.Transport, conf.Verbose, opts.ConfigPath)
	if err != nil {
		return err
	}
	if addr := strings.TrimSpace(transport.LocalAddr()); addr != "" {
		ui.SetAccentAddresses(addr, "")
		homeSide.setSelf(addr)
		ui.HomeRefresh()
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

	homeSide.setSelf(localAddr)
	// Our card goes to whoever we talk to, and theirs comes to us, into the
	// book (a known address's additions written, an unknown one's card
	// waiting to be confirmed). The chat is only what people write.
	tcpsignal.SetCards(homeSide.store.MyCard, func(host string, card []byte) {
		homeSide.store.Received(host, card)
	})
	if opts.FriendName != "" {
		d, ok := homeSide.lookup(opts.FriendName)
		if !ok {
			return fmt.Errorf("%q is not in the contacts", opts.FriendName)
		}
		if opts.DialAddr == "" {
			opts.DialAddr = d.Addr()
		}
	}

	var mediaFactory tcpsignal.MediaSessionFactory
	if udpConn != nil {
		mediaFactory = func(peerHost string, remote *net.UDPAddr, peer tcpsignal.PeerInfo) (tcpsignal.MediaSession, error) {
			// Every call starts with the default audio buffers (-audio
			// changes last one call). Where the peer is only matters for
			// the log: a LAN peer means multicast found a direct link.
			udptransport.ResetAudioSettings()
			ui.SetAccentAddresses(localAddr, peerHost)
			homeSide.noted(peerHost)
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

	// Calls are taken and made from the home screen. One by an address on
	// the command line redials while the line drops, until hung up; then
	// the home screen. Its port may be taken (a server of the same
	// machine): it calls out all the same.
	tcp, err := transport.ListenTCP(opts.ListenPort)
	if err == nil {
		stopServer, serr := tcpsignal.StartSignalServerTCP(tcp, homeSide.nameOf, opts.ListenPort, termSync, mediaFactory)
		if serr != nil {
			err = serr
		} else {
			defer stopServer()
		}
	}
	if err != nil {
		if opts.DialAddr == "" {
			return fmt.Errorf("listen failed: %w", err)
		}
		log.Printf("[p2p] not taking calls: listen failed: %v", err)
	} else if localAddr != "" {
		log.Printf("[hint] %s", formatSayHint(opts.Transport, localAddr, opts.ListenPort))
	}
	homeSide.setDial(func(ctx context.Context, d contacts.Device) error {
		return dialOnce(ctx, d, termSync, mediaFactory, transport.DialTCP)
	})
	// Who of the contacts is online: asked now and then (presence.go).
	go homeSide.watchPresence(appCtx, func(host string, port int) bool {
		return tcpsignal.Probe(host, port, transport.DialTCP, presenceTimeout)
	})
	if opts.DialAddr != "" {
		host, port, err := conf.ParseDialTarget(opts.DialAddr, opts.ListenPort)
		if err != nil {
			return fmt.Errorf("dial target: %w", err)
		}
		homeSide.place(contacts.Device{Host: host, Port: port}, func(ctx context.Context, d contacts.Device) error {
			return autodial(ctx, d.Host, d.Port, termSync, mediaFactory, transport.DialTCP)
		})
	}

	<-appCtx.Done()
	log.Println("shutting down")
	homeSide.hangup()
	return nil
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
			stop   func() error
			done   <-chan struct{}
			hungUp func() bool
			err    error
		}
		dialed := make(chan dialResult, 1)
		ring := &ringer{}
		go func() {
			stop, done, hungUp, err := tcpsignal.StartSignalClientTCP(ctx, conf.HostnameOr("me"), host, port, termSync, mediaFactory, dial, func() {
				ui.SetStatusMessage(lang.T(lang.Dialing) + "\n" + target + "\n" + lang.T(lang.RingingThere))
				ring.start() // it rings there
			})
			ring.end()
			dialed <- dialResult{stop, done, hungUp, err}
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
				if res.hungUp() { // hung up, not lost: no redial, the home screen
					log.Printf("[p2p] %s hung up: the call is over", target)
					return nil
				}
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
		if errors.Is(err, tcpsignal.ErrDeclined) || errors.Is(err, tcpsignal.ErrNoAnswer) {
			// Not taken: no redial.
			log.Printf("[p2p] %s: %v", target, err)
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

// dialOnce makes a call from the home screen: a few tries while the line
// cannot be reached, then the call until either side hangs up or it drops
// (no redial: the home screen is back). ctx ends it at any time (Esc).
func dialOnce(ctx context.Context, d contacts.Device, termSync *tcpsignal.TermSizeSync, mediaFactory tcpsignal.MediaSessionFactory, dial tcpsignal.TCPDialFunc) error {
	const tries = 3
	target := formatPeerAddr(d.Host, d.Port)
	ring := &ringer{}
	ring.start() // from the first moment of «Вызов» to the answer, or the end
	defer ring.end()
	for attempt := 1; ; attempt++ {
		// One call to the user: its tries are not shown (a mesh route is
		// often found only on the second).
		ui.SetStatusMessage(lang.T(lang.Dialing) + "\n" + target + "\n" + lang.T(lang.Punching))
		type dialResult struct {
			stop   func() error
			done   <-chan struct{}
			hungUp func() bool
			err    error
		}
		dialed := make(chan dialResult, 1)
		go func() {
			stop, done, hungUp, err := tcpsignal.StartSignalClientTCP(ctx, conf.HostnameOr("me"), d.Host, d.Port, termSync, mediaFactory, dial, func() {
				ui.SetStatusMessage(lang.T(lang.Dialing) + "\n" + target + "\n" + lang.T(lang.RingingThere))
			})
			dialed <- dialResult{stop, done, hungUp, err}
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
		if res.err == nil {
			ring.end() // answered: the call's sound
			log.Printf("[p2p] dial established to %s", target)
			select {
			case <-ctx.Done():
				res.stop()
			case <-res.done:
				res.stop()
			}
			log.Printf("[p2p] the call with %s is over", target)
			return nil
		}
		var incompatible *tcpsignal.IncompatibleError
		switch {
		case errors.As(res.err, &incompatible):
			ui.SetStatusMessage(res.err.Error())
			return res.err
		case failed(res.err): // busy, declined, unanswered: the home screen at once
			return res.err
		case attempt >= tries:
			ui.SetStatusMessage(lang.T(lang.NoCarrier) + "\n" + target + "\n" + res.err.Error())
			return res.err
		}
		log.Printf("[p2p] dial %s failed: %v", target, res.err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
}

// ringer plays the ring for the caller: start begins it (once), end stops
// it for good; either may come from another goroutine, end before start.
type ringer struct {
	mu   sync.Mutex
	stop func()
	done bool
}

func (r *ringer) start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.done && r.stop == nil {
		r.stop = device.PlayTone(tones.Ring(), tones.Rate, true)
	}
}

func (r *ringer) end() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done = true
	if r.stop != nil {
		r.stop()
		r.stop = nil
	}
}

// failed reports whether a call was not taken there: the line busy, the
// call declined or unanswered. No redial: the home screen comes back at
// once.
func failed(err error) bool {
	return errors.Is(err, tcpsignal.ErrPeerBusy) || errors.Is(err, tcpsignal.ErrDeclined) || errors.Is(err, tcpsignal.ErrNoAnswer)
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
