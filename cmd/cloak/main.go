package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/PrateekKumar1709/cloak/internal/config"
	"github.com/PrateekKumar1709/cloak/internal/detect"
	"github.com/PrateekKumar1709/cloak/internal/entmap"
	"github.com/PrateekKumar1709/cloak/internal/lemonade"
	"github.com/PrateekKumar1709/cloak/internal/policy"
	"github.com/PrateekKumar1709/cloak/internal/proxy"
)

var version = "1.0.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "start":
		os.Exit(cmdStart(args))
	case "stop":
		os.Exit(cmdStop())
	case "status":
		os.Exit(cmdStatus())
	case "tail":
		os.Exit(cmdTail())
	case "test":
		os.Exit(cmdTest(args))
	case "doctor":
		os.Exit(cmdDoctor())
	case "pull-model":
		os.Exit(cmdPullModel(args))
	case "demo":
		os.Exit(cmdDemo(args))
	case "version", "-v", "--version":
		fmt.Printf("cloak %s\n", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Cloak: local PII firewall for cloud AI (v%s)

Usage:
  cloak start [--config path] [--demo] [--no-lemonade]
                                              Start the gateway
                                              --demo = mock cloud upstream (no API key)
  cloak stop                                   Stop a background gateway
  cloak status                                 Check if Cloak is running
  cloak tail                                   Live audit log (SSE)
  cloak test "text with PII"                   Show what would be redacted
  cloak doctor                                 Check Lemonade, model, keys
  cloak pull-model [name]                      Pull / load a Lemonade model
  cloak demo                                   Lemonade + mock cloud (no API key needed)
  cloak version

Env:
  OPENAI_API_KEY / ANTHROPIC_API_KEY          Upstream credentials (not needed with --demo)
  CLOAK_CONFIG                                 Config file path

`, version)
}

func loadConfig(args []string) (*config.Config, error) {
	path := os.Getenv("CLOAK_CONFIG")
	for i := 0; i < len(args); i++ {
		if args[i] == "--config" && i+1 < len(args) {
			path = args[i+1]
			i++
		}
	}
	return config.Load(path)
}

func pidFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "cloak", "cloak.pid")
}

func cmdStart(args []string) int {
	cfg, err := loadConfig(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	demo := false
	for _, a := range args {
		if a == "--no-lemonade" {
			cfg.Lemonade.Enabled = false
		}
		if a == "--demo" {
			demo = true
		}
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}))

	var demoShutdown func()
	if demo {
		base, anth, shutdown, err := proxy.StartDemoUpstream()
		if err != nil {
			fmt.Fprintf(os.Stderr, "demo upstream: %v\n", err)
			return 1
		}
		demoShutdown = shutdown
		cfg.Upstream.BaseURL = base
		cfg.Upstream.APIKey = "cloak-demo"
		cfg.Upstream.AnthropicBaseURL = anth
		cfg.Upstream.AnthropicAPIKey = "cloak-demo"
		log.Info("demo mode: mock cloud upstream", "openai", base)
	}

	if cfg.Lemonade.Enabled {
		ensureLemonade(cfg, log)
	}

	srv, err := proxy.New(cfg, log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: %v\n", err)
		return 1
	}

	_ = os.MkdirAll(filepath.Dir(pidFile()), 0o700)
	_ = os.WriteFile(pidFile(), []byte(strconv.Itoa(os.Getpid())), 0o600)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	fmt.Printf("Cloak listening on http://%s\n", cfg.Listen)
	fmt.Printf("  Dashboard:  http://%s\n", cfg.Listen)
	fmt.Printf("  OpenAI:     export OPENAI_BASE_URL=http://%s/v1\n", cfg.Listen)
	fmt.Printf("  Anthropic:  export ANTHROPIC_BASE_URL=http://%s/anthropic\n", cfg.Listen)
	if demo {
		fmt.Println("  Mode:       DEMO (mock cloud upstream; Lemonade is the detector)")
	}
	if cfg.Lemonade.OmniASR {
		fmt.Printf("  Omni ASR:   POST http://%s/v1/audio/transcriptions  (Lemonade Whisper)\n", cfg.Listen)
	}

	select {
	case <-ctx.Done():
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
		if demoShutdown != nil {
			demoShutdown()
		}
		_ = os.Remove(pidFile())
		fmt.Println("stopped")
		return 0
	case err := <-errCh:
		if demoShutdown != nil {
			demoShutdown()
		}
		_ = os.Remove(pidFile())
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "server: %v\n", err)
			return 1
		}
	}
	return 0
}

func cmdDemo(args []string) int {
	fmt.Println("Cloak demo")
	fmt.Println("----------")
	fmt.Println("1) Starting Lemonade (local detector + Whisper)…")
	if script := findRepoScript("scripts/start-lemonade.sh"); script != "" {
		cmd := exec.Command("bash", script)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	} else {
		fmt.Println("   (run ./scripts/start-lemonade.sh if Lemonade is not up)")
	}
	// ensure whisper model
	cfg, _ := loadConfig(args)
	if cfg == nil {
		cfg = config.Default()
	}
	client := lemonade.New(cfg.Lemonade.BaseURL, cfg.Lemonade.Model, 30*time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := client.Healthy(ctx); err == nil {
		wm := cfg.Lemonade.WhisperModel
		if wm == "" {
			wm = "Whisper-Tiny"
		}
		fmt.Printf("2) Ensuring Whisper model %s…\n", wm)
		_ = client.Pull(ctx, wm)
	}
	fmt.Println("3) Launching Cloak with --demo (mock cloud upstream, real Lemonade NER)…")
	fmt.Println()
	fmt.Println("Open http://127.0.0.1:7777  then try:")
	fmt.Println()
	fmt.Println("  # 1. redaction: PII is pseudonymized before it reaches the cloud")
	fmt.Println(`  curl http://127.0.0.1:7777/v1/chat/completions \`)
	fmt.Println(`    -H 'Content-Type: application/json' \`)
	fmt.Println(`    -d '{"model":"demo","messages":[{"role":"user","content":"Email ada@analeng.org about Project Nightingale on db-prod-03"}]}'`)
	fmt.Println()
	fmt.Println("  # 2. Private Mode: a crown-jewel secret is answered ON-DEVICE, never sent to cloud")
	fmt.Println(`  curl http://127.0.0.1:7777/v1/chat/completions \`)
	fmt.Println(`    -H 'Content-Type: application/json' \`)
	fmt.Println(`    -d '{"model":"demo","messages":[{"role":"user","content":"Is this AWS key valid: AKIAIOSFODNN7EXAMPLE ?"}]}'`)
	fmt.Println("  # -> response header X-Cloak-Route: local-private")
	fmt.Println()
	return cmdStart(append(args, "--demo"))
}

func findRepoScript(rel string) string {
	if wd, err := os.Getwd(); err == nil {
		p := filepath.Join(wd, rel)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "..", rel)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func ensureLemonade(cfg *config.Config, log *slog.Logger) {
	client := lemonade.New(cfg.Lemonade.BaseURL, cfg.Lemonade.Model, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Healthy(ctx); err == nil {
		log.Info("lemonade reachable", "url", cfg.Lemonade.BaseURL)
		ensureModelLoaded(client, cfg, log)
		return
	}

	log.Warn("lemonade not reachable; attempting to start local lemond")
	if started := startLocalLemond(log); started {
		time.Sleep(1500 * time.Millisecond)
		ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel2()
		if err := client.Healthy(ctx2); err == nil {
			log.Info("lemonade started", "url", cfg.Lemonade.BaseURL)
			ensureModelLoaded(client, cfg, log)
			return
		}
	}

	// Fall back to PATH binaries (full installer)
	for _, cand := range []struct{ bin, arg string }{
		{"lemond", ""},
		{"lemonade-server", "serve"},
	} {
		path, err := exec.LookPath(cand.bin)
		if err != nil {
			continue
		}
		var cmd *exec.Cmd
		if cand.arg == "" {
			cmd = exec.Command(path, "--host", "127.0.0.1", "--port", "13305")
		} else {
			cmd = exec.Command(path, cand.arg)
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			log.Warn("failed to start lemonade", "bin", cand.bin, "err", err)
			continue
		}
		log.Info("started lemonade process", "bin", cand.bin, "pid", cmd.Process.Pid)
		time.Sleep(2 * time.Second)
		ensureModelLoaded(client, cfg, log)
		return
	}
	log.Warn("lemonade not found; Tier-2 NER unavailable. Run ./scripts/start-lemonade.sh or install from https://lemonade-server.ai")
}

func startLocalLemond(log *slog.Logger) bool {
	// Prefer repo-bundled embeddable build: <repo>/tools/lemonade/lemond
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(exe), "tools", "lemonade", "lemond"),
			filepath.Join(filepath.Dir(exe), "..", "tools", "lemonade", "lemond"),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "tools", "lemonade", "lemond"))
	}
	for _, p := range candidates {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			continue
		}
		cmd := exec.Command(p, "--host", "127.0.0.1", "--port", "13305")
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Start(); err != nil {
			log.Warn("start lemond failed", "path", p, "err", err)
			continue
		}
		log.Info("started bundled lemond", "path", p, "pid", cmd.Process.Pid)
		_ = os.MkdirAll(filepath.Dir(pidFile()), 0o700)
		_ = os.WriteFile(filepath.Join(filepath.Dir(pidFile()), "lemonade.pid"),
			[]byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
		return true
	}
	return false
}

func ensureModelLoaded(client *lemonade.Client, cfg *config.Config, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	loaded, err := client.LoadedModel(ctx)
	if err == nil && loaded == cfg.Lemonade.Model {
		log.Info("detector model ready", "model", loaded)
		return
	}
	log.Info("ensuring detector model", "model", cfg.Lemonade.Model)
	if err := client.Pull(ctx, cfg.Lemonade.Model); err != nil {
		log.Warn("pull model failed", "err", err)
	}
	if err := client.Load(ctx, cfg.Lemonade.Model); err != nil {
		log.Warn("load model failed", "err", err)
		return
	}
	log.Info("detector model loaded", "model", cfg.Lemonade.Model)
}

func cmdStop() int {
	data, err := os.ReadFile(pidFile())
	if err != nil {
		fmt.Println("cloak is not running (no pid file)")
		return 1
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if pid <= 0 {
		fmt.Println("invalid pid file")
		return 1
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Println("not running")
		_ = os.Remove(pidFile())
		return 1
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "stop: %v\n", err)
		return 1
	}
	_ = os.Remove(pidFile())
	fmt.Println("sent SIGTERM")
	return 0
}

func cmdStatus() int {
	cfg, _ := loadConfig(nil)
	addr := "127.0.0.1:7777"
	if cfg != nil {
		addr = cfg.Listen
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Println("cloak: down")
		return 1
	}
	defer resp.Body.Close()
	fmt.Printf("cloak: up (%s)\n", addr)
	return 0
}

func cmdTail() int {
	cfg, _ := loadConfig(nil)
	addr := "127.0.0.1:7777"
	if cfg != nil {
		addr = cfg.Listen
	}
	resp, err := http.Get("http://" + addr + "/api/events")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tail: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	_, err = io.Copy(os.Stdout, resp.Body)
	if err != nil && err != io.EOF {
		return 1
	}
	return 0
}

func cmdTest(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: cloak test "text with john@acme.com"`)
		return 1
	}
	text := strings.Join(args, " ")
	// Prefer live server if up
	cfg, _ := loadConfig(nil)
	addr := "127.0.0.1:7777"
	if cfg != nil {
		addr = cfg.Listen
	}
	resp, err := http.Post("http://"+addr+"/api/test", "application/json", strings.NewReader(`{"text":`+jsonQuote(text)+`}`))
	if err == nil {
		defer resp.Body.Close()
		_, _ = io.Copy(os.Stdout, resp.Body)
		fmt.Println()
		return 0
	}

	// Offline local scan (Tier-1 only)
	if cfg == nil {
		cfg = config.Default()
	}
	cfg.Lemonade.Enabled = false
	pipe := detect.NewPipeline(detect.PipelineConfig{
		Watchlist: cfg.Watchlist,
		Allowlist: cfg.Allowlist,
	}, nil)
	res := pipe.Scan(context.Background(), text)
	eng := policy.New(cfg.Policy)
	dec := eng.Evaluate(res.Findings)
	store := entmap.NewStore()
	store.GetOrCreate("cli")
	toReplace := append(append([]detect.Finding{}, dec.Redact...), dec.Blocked...)
	san, applied := entmap.ApplyFindings(text, toReplace, "cli", store)
	out := map[string]any{
		"findings":   res.Findings,
		"latency_ms": res.Latency,
		"blocked":    !dec.Allowed,
		"sanitized":  san,
		"applied":    applied,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
	return 0
}

func cmdDoctor() int {
	cfg, err := loadConfig(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	ok := true
	fmt.Println("Cloak doctor")
	fmt.Println("-----------")

	check := func(name string, err error) {
		if err != nil {
			fmt.Printf("✗ %s: %v\n", name, err)
			ok = false
			return
		}
		fmt.Printf("✓ %s\n", name)
	}

	// config
	check("config loaded", nil)
	fmt.Printf("  listen=%s lemonade=%s model=%s\n", cfg.Listen, cfg.Lemonade.BaseURL, cfg.Lemonade.Model)

	// keys (optional until cloud upstream is configured)
	if cfg.UpstreamAPIKey() == "" {
		fmt.Println("· OPENAI_API_KEY not set (ok for local-only / mock upstream)")
	} else {
		fmt.Println("✓ OpenAI-compatible upstream key present")
	}
	if cfg.AnthropicKey() == "" {
		fmt.Println("· ANTHROPIC_API_KEY not set (optional)")
	} else {
		fmt.Println("✓ Anthropic key present")
	}

	// lemonade
	client := lemonade.New(cfg.Lemonade.BaseURL, cfg.Lemonade.Model, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Healthy(ctx); err != nil {
		fmt.Printf("✗ lemonade server: %v\n", err)
		fmt.Println("  Start with: ./scripts/start-lemonade.sh")
		fmt.Println("  Or install: https://lemonade-server.ai")
		ok = false
	} else {
		fmt.Printf("✓ lemonade server reachable at %s\n", cfg.Lemonade.BaseURL)
		if loaded, err := client.LoadedModel(ctx); err != nil {
			fmt.Printf("· could not read loaded model: %v\n", err)
		} else if loaded == "" {
			fmt.Printf("· no model loaded; run: cloak pull-model %s\n", cfg.Lemonade.Model)
		} else if loaded != cfg.Lemonade.Model {
			fmt.Printf("· loaded model is %s (config wants %s)\n", loaded, cfg.Lemonade.Model)
		} else {
			fmt.Printf("✓ detector model loaded: %s\n", loaded)
		}
		if models, err := client.ListModels(ctx, true); err == nil {
			fmt.Printf("  downloaded models: %d\n", len(models))
		}
	}

	// cloak itself
	resp, err := http.Get("http://" + cfg.Listen + "/healthz")
	if err != nil {
		fmt.Println("· cloak gateway not running (start with `cloak start`)")
	} else {
		resp.Body.Close()
		fmt.Printf("✓ cloak gateway up on %s\n", cfg.Listen)
	}

	if !ok {
		return 1
	}
	fmt.Println("\nAll critical checks passed.")
	return 0
}

func cmdPullModel(args []string) int {
	cfg, err := loadConfig(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	model := cfg.Lemonade.Model
	if len(args) > 0 {
		model = args[0]
	}
	client := lemonade.New(cfg.Lemonade.BaseURL, model, 30*time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := client.Healthy(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "lemonade not reachable at %s: %v\n", cfg.Lemonade.BaseURL, err)
		fmt.Fprintln(os.Stderr, "Start it with: ./scripts/start-lemonade.sh")
		return 1
	}
	fmt.Printf("Pulling %s…\n", model)
	if err := client.Pull(ctx, model); err != nil {
		fmt.Fprintf(os.Stderr, "pull failed: %v\n", err)
		return 1
	}
	fmt.Printf("Loading %s…\n", model)
	if err := client.Load(ctx, model); err != nil {
		fmt.Fprintf(os.Stderr, "load failed: %v\n", err)
		return 1
	}
	fmt.Println("done")
	return 0
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
