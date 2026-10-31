package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ieuforu/little-garden/server/internal/assets"
	"github.com/ieuforu/little-garden/server/internal/garden"
	"github.com/ieuforu/little-garden/server/internal/httpapi"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	var err error
	switch command {
	case "serve":
		err = serve()
	case "backup":
		err = backup(os.Args[2:])
	case "admin":
		err = admin(os.Args[2:])
	case "healthcheck":
		client := http.Client{Timeout: 2 * time.Second}
		var response *http.Response
		_, port, e := net.SplitHostPort(env("LISTEN_ADDR", ":8080"))
		if e != nil {
			err = e
			break
		}
		response, err = client.Get("http://127.0.0.1:" + port + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode != 200 {
				err = errors.New("server is not ready")
			}
		}
	case "keygen":
		var data [32]byte
		_, err = rand.Read(data[:])
		if err == nil {
			fmt.Println(hex.EncodeToString(data[:]))
		}
	case "schema":
		fmt.Println(1)
	case "db-version":
		var version int
		version, err = garden.SchemaVersion(context.Background(), env("DATABASE_PATH", "/data/garden.db"))
		if err == nil {
			fmt.Println(version)
		}
	default:
		err = errors.New("usage: garden [serve | backup --output FILE | admin COMMAND [VALUE] | healthcheck | keygen]")
	}
	if err != nil {
		slog.Error("garden stopped", "error", err)
		os.Exit(1)
	}
}

func serve() error {
	cfg, err := httpapi.LoadConfig()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(cfg.Database), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(cfg.Database+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another garden process already owns this database")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := garden.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer store.Close()
	engine, err := garden.New(ctx, store)
	if err != nil {
		return err
	}
	api := httpapi.New(cfg, engine, store, assets.Files())
	defer api.Shutdown()
	server := &http.Server{Addr: cfg.Listen, Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	limited := &limitedListener{Listener: listener, slots: make(chan struct{}, cfg.MaxConnections+128)}
	adminServer, adminListener, err := adminServer(cfg.AdminSocket, engine)
	if err != nil {
		listener.Close()
		return err
	}
	defer adminListener.Close()
	defer os.Remove(cfg.AdminSocket)
	failed := make(chan error, 2)
	go func() { failed <- server.Serve(limited) }()
	go func() { failed <- adminServer.Serve(adminListener) }()
	slog.Info("garden started", "address", cfg.Listen, "development", cfg.Development)
	select {
	case <-ctx.Done():
	case err = <-failed:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	api.Shutdown()
	shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = adminServer.Shutdown(shutdown)
	return server.Shutdown(shutdown)
}

type limitedListener struct {
	net.Listener
	slots chan struct{}
}
type limitedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *limitedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &limitedConn{Conn: connection, release: func() { <-l.slots }}, nil
		default:
			connection.Close()
		}
	}
}

func backup(args []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	output := flags.String("output", "", "path of the new backup file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *output == "" {
		return errors.New("backup requires --output")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := garden.Backup(ctx, env("DATABASE_PATH", "/data/garden.db"), *output); err != nil {
		return err
	}
	fmt.Println("Verified backup:", *output)
	return nil
}

type adminRequest struct {
	Command string `json:"command"`
	Value   string `json:"value"`
}

func adminServer(path string, engine *garden.Engine) (*http.Server, net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, nil, errors.New("ADMIN_SOCKET points to a non-socket file")
		}
		if err = os.Remove(path); err != nil {
			return nil, nil, err
		}
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /command", func(w http.ResponseWriter, r *http.Request) {
		var req adminRequest
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		defer r.Body.Close()
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "invalid command", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		var err error
		switch req.Command {
		case "pause":
			err = engine.Pause(ctx, true)
		case "resume":
			err = engine.Pause(ctx, false)
		case "hide":
			err = engine.Hide(ctx, req.Value, true)
		case "show":
			err = engine.Hide(ctx, req.Value, false)
		case "ban-flower":
			err = engine.BanFlower(ctx, req.Value)
		case "new-garden":
			err = engine.NewGarden(ctx)
		default:
			http.Error(w, "unknown command", 400)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(engine.Snapshot())
	})
	return &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 2048}, listener, nil
}

func admin(args []string) error {
	if len(args) == 0 {
		return errors.New("admin requires pause, resume, hide ID, show ID, ban-flower ID, or new-garden")
	}
	value := ""
	if len(args) > 1 {
		value = args[1]
	}
	body, _ := json.Marshal(adminRequest{Command: args[0], Value: value})
	client := http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", env("ADMIN_SOCKET", "/data/admin.sock"))
	}}}
	response, err := client.Post("http://unix/command", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("admin: %s", data)
	}
	fmt.Println("Applied:", args[0])
	return nil
}
