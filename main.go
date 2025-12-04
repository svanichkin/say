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
	"say/conf"
	"say/logs"
	"say/network"
	tcpsignal "say/network/tcp"
	udptransport "say/network/udp"
	"say/ui"
	"strings"
	"syscall"
	"time"

	ygg "github.com/svanichkin/Ygg"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "say: %v\n", err)
		os.Exit(1)
	}
}

func run() (err error) {
	opts, err := conf.ParseCLI()
	if err != nil {
		return err
	}

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
	if opts.DialAddr != "" {
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

	friends, loadErr := conf.LoadFriendsFromConfig(opts.ConfigPath)
	if loadErr != nil {
		log.Printf("[cfg] couldn't read friends from %s: %v", opts.ConfigPath, loadErr)
	} else if len(friends) > 0 {
		logs.LogV("[cfg] friends loaded:")
		if conf.Verbose {
			for _, f := range friends {
				log.Printf("  - %s <%s>", f.Name, f.Address)
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
	if port != network.DefaultListenPort && port > 0 {
		quoted := host
		if strings.Contains(host, ":") {
			quoted = fmt.Sprintf("[%s]", host)
		}
		return fmt.Sprintf("say \"%s\" %d", quoted, port)
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
