// Command cia-monitor serves a read-only browser page that shows what the local
// AI server is doing: the request in flight, its speed, and the GPU, memory
// and disk it runs on. It reads the edge's public control-plane routes and the
// machine's performance counters, holds no credential, and listens on
// loopback only.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Sitr3n01/local-ai-provider/internal/adminpipe"
	"github.com/Sitr3n01/local-ai-provider/internal/monitor"
)

var version = "dev"

// defaultEdgeExecutable is the installed edge every deployment script pins; it
// is the only process the monitor accepts as the owner of the administrative
// pipe.
const defaultEdgeExecutable = `C:\IA\local-ai-v2\bin\cia-edge.exe`

// Each deployment's monitor sits beside its edge: canary on the 18xxx ports,
// final on the 8xxx ones, so both can be watched at once.
var environmentDefaults = map[string]struct {
	listen, control, data string
}{
	"canary": {"127.0.0.1:18095", "http://127.0.0.1:18091", "http://127.0.0.1:18090"},
	"final":  {"127.0.0.1:8095", "http://127.0.0.1:8091", "http://127.0.0.1:8090"},
}

func main() {
	if err := run(); err != nil {
		logEvent("fatal", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("cia-monitor", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	environment := flags.String("environment", "canary", "deployment to watch: canary or final")
	listen := flags.String("listen", "", "loopback address the page is served on (default: 127.0.0.1:18095 canary, 127.0.0.1:8095 final)")
	control := flags.String("control-url", "", "the edge's control-plane URL (default: the environment's)")
	data := flags.String("data-url", "", "the edge's data-plane URL, shown to clients and never contacted (default: the environment's)")
	adminPipe := flags.String("admin-pipe", "auto", `the edge's administrative pipe for the load and unload buttons: "auto" for the environment's, "off" to disable them, or an explicit \\.\pipe\ path`)
	adminServer := flags.String("admin-server", defaultEdgeExecutable, "the edge executable that must own the administrative pipe")
	open := flags.Bool("open", false, "open the page in the default browser")
	showVersion := flags.Bool("version", false, "print the version and exit")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}

	defaults, ok := environmentDefaults[*environment]
	if !ok {
		return errors.New("--environment must be canary or final")
	}
	if *listen == "" {
		*listen = defaults.listen
	}
	if *control == "" {
		*control = defaults.control
	}
	if *data == "" {
		*data = defaults.data
	}
	if err := monitor.ValidateListenAddr(*listen); err != nil {
		return err
	}
	controlURL, err := monitor.ValidateEdgeURL("--control-url", *control)
	if err != nil {
		return err
	}
	dataURL, err := monitor.ValidateEdgeURL("--data-url", *data)
	if err != nil {
		return err
	}
	controls, err := controlOptions(*environment, *adminPipe, *adminServer)
	if err != nil {
		return err
	}
	page := "http://" + *listen + "/"

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		// Launching the monitor twice should show the page, not an error: when
		// the address is held by a monitor already, open that one.
		if *open && isMonitor(*listen) {
			logEvent("already_running", map[string]any{"page": page})
			return openBrowser(page)
		}
		return fmt.Errorf("listen on %s: %w", *listen, err)
	}

	collector := monitor.NewCollector(monitor.Options{
		Version:     version,
		Environment: *environment,
		ControlURL:  controlURL,
		DataURL:     dataURL,
		Control:     controls,
		Log:         logEvent,
	})
	handler, err := monitor.NewHandler(collector, *listen)
	if err != nil {
		_ = listener.Close()
		return err
	}
	server := monitor.Server(handler)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go collector.Run(ctx)

	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	logEvent("starting", map[string]any{
		"page":        page,
		"environment": *environment,
		"control_url": controlURL.String(),
		"admin_pipe":  controls.Pipe,
		"version":     version,
	})
	if *open {
		if err := openBrowser(page); err != nil {
			logEvent("open_failed", map[string]any{"error": err.Error()})
		}
	}

	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}

// controlOptions resolves the administrative pipe the same way the edge names
// it, so a canary monitor can only ever reach the canary edge. The server path
// is what the pipe client checks the pipe's owner against: without it, whoever
// created the name first could answer.
func controlOptions(environment, pipe, server string) (monitor.ControlOptions, error) {
	switch pipe = strings.TrimSpace(pipe); pipe {
	case "off":
		return monitor.ControlOptions{}, nil
	case "auto", "":
		// Empty off Windows, where the pipe does not exist: the buttons then
		// report themselves disabled.
		pipe = adminpipe.DefaultName(environment)
	default:
		if !strings.HasPrefix(pipe, `\\.\pipe\`) {
			return monitor.ControlOptions{}, errors.New(`--admin-pipe must be "auto", "off" or a \\.\pipe\ path`)
		}
	}
	server = strings.TrimSpace(server)
	if !filepath.IsAbs(server) || !strings.EqualFold(filepath.Ext(server), ".exe") {
		return monitor.ControlOptions{}, errors.New("--admin-server must be the absolute path of the edge executable")
	}
	return monitor.ControlOptions{Pipe: pipe, Server: server}, nil
}

// isMonitor asks whatever holds the address whether it is a monitor. Only a
// snapshot that decodes as one counts, so an unrelated program on the port is
// reported as the listen error it is.
func isMonitor(listen string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	for attempt := 0; attempt < 3; attempt++ {
		response, err := client.Get("http://" + listen + "/api/snapshot")
		if err != nil {
			return false
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if response.StatusCode == http.StatusServiceUnavailable {
			time.Sleep(time.Second)
			continue
		}
		if readErr != nil || response.StatusCode != http.StatusOK {
			return false
		}
		var snapshot struct {
			Monitor struct {
				Version string `json:"version"`
			} `json:"monitor"`
		}
		return json.Unmarshal(body, &snapshot) == nil && snapshot.Monitor.Version != ""
	}
	return false
}

func logEvent(event string, fields map[string]any) {
	record := map[string]any{
		"time":    time.Now().UTC().Format(time.RFC3339Nano),
		"service": "cia-monitor",
		"event":   event,
	}
	for key, value := range fields {
		record[key] = value
	}
	_ = json.NewEncoder(os.Stderr).Encode(record)
}
