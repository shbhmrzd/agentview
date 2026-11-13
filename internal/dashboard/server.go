package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shbhmrzd/agentview/internal/observe"
)

//go:embed web/*
var assets embed.FS

type Options struct {
	Roots          []Root
	IncludeContent bool
	MaxRecordBytes int
	Poll           time.Duration
}

type Server struct {
	Store        *Store
	options      Options
	token        string
	authority    string
	historySlots chan struct{}
}

func NewServer(options Options, authority string) (*Server, error) {
	if options.Poll < 100*time.Millisecond {
		return nil, fmt.Errorf("poll must be at least 100ms")
	}
	if options.MaxRecordBytes < 1 || options.MaxRecordBytes > observe.MaxRecordBytes {
		return nil, fmt.Errorf("invalid maximum record size")
	}
	if len(options.Roots) == 0 {
		return nil, fmt.Errorf("at least one session directory is required")
	}
	for i := range options.Roots {
		root := &options.Roots[i]
		if root.Agent != "claude" && root.Agent != "codex" {
			return nil, fmt.Errorf("dashboard supports claude and codex")
		}
		canonical, err := filepath.EvalSymlinks(root.Path)
		if err == nil {
			root.Path = canonical
		}
		root.Path, err = filepath.Abs(root.Path)
		if err != nil {
			return nil, err
		}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return &Server{Store: NewStore(options.IncludeContent), options: options, token: hex.EncodeToString(secret), authority: authority, historySlots: make(chan struct{}, 2)}, nil
}

func (s *Server) URL() string { return "http://" + s.authority + "/#token=" + s.token }

// Watch owns observers only; cancellation neither signals nor writes to agents.
func (s *Server) Watch(ctx context.Context) {
	var wg sync.WaitGroup
	for _, root := range s.options.Roots {
		root := root
		wg.Add(1)
		go func() {
			defer wg.Done()
			root.Status = "indexing"
			s.Store.SetRoot(root)
			directory, err := observe.NewDirectory(observe.DirectoryOptions{Agent: root.Agent, Root: root.Path, History: "all", Normalizer: observe.NormalizerOptions{IncludeContent: s.options.IncludeContent}, MaxRecordBytes: s.options.MaxRecordBytes})
			if err == nil {
				err = directory.Run(ctx, s.options.Poll, func(e observe.Event) error { s.Store.Apply(e); return ctx.Err() })
			}
			if err != nil && ctx.Err() == nil {
				root.Status, root.Detail = "unavailable", err.Error()
				s.Store.SetRoot(root)
			}
		}()
	}
	wg.Wait()
}

func (s *Server) Handler() http.Handler {
	static, _ := fs.Sub(assets, "web")
	files := http.FileServer(http.FS(static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		// Strict authority + origin + per-launch bearer token keep private local
		// transcripts out of unrelated websites and DNS rebinding requests.
		if r.Host != s.authority {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+s.authority {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "read-only API", http.StatusMethodNotAllowed)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
				http.Error(w, "Open the complete URL printed by AgentView to connect.", http.StatusUnauthorized)
				return
			}
			s.api(w, r)
			return
		}
		switch r.URL.Path {
		case "/", "/app.js", "/style.css", "/helpers.mjs", "/tour.mjs", "/theme.js", "/themes.css", "/conversations.mjs":
			files.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/sessions" {
		writeJSON(w, s.Store.Snapshot())
		return
	}
	if r.URL.Path == "/api/agents" {
		tree, ok := s.Store.AgentTree(r.URL.Query().Get("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, tree)
		return
	}
	if r.URL.Path != "/api/session" && r.URL.Path != "/api/history" {
		http.NotFound(w, r)
		return
	}
	session, entries, ok := s.Store.Session(r.URL.Query().Get("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/api/session" {
		writeJSON(w, struct {
			Session Session `json:"session"`
			Events  []Entry `json:"events"`
		}{session, entries})
		return
	}
	select {
	case s.historySlots <- struct{}{}:
		defer func() { <-s.historySlots }()
	default:
		http.Error(w, "History reader is busy; try again shortly.", http.StatusTooManyRequests)
		return
	}
	var rootPath string
	for _, root := range s.options.Roots {
		if root.Agent == session.Agent {
			rootPath = root.Path
			break
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result := struct {
		Events      []Entry `json:"events"`
		Limited     bool    `json:"limited"`
		Dropped     int     `json:"dropped"`
		Diagnostics int     `json:"diagnostics"`
		ReadAt      string  `json:"read_at"`
	}{Events: []Entry{}, ReadAt: time.Now().UTC().Format(time.RFC3339Nano)}
	metrics := Metrics{}
	limited, err := observe.ReadHistory(ctx, rootPath, session.Path, session.Agent, observe.NormalizerOptions{IncludeContent: s.options.IncludeContent}, s.options.MaxRecordBytes, 64*1024*1024, func(e observe.Event) {
		duration := metrics.apply(e)
		if entry, ok := displayEvent(e); ok {
			entry.DurationMS = duration
			result.Events = append(result.Events, entry)
			if len(result.Events) > 200 {
				result.Events[0] = Entry{}
				result.Events = result.Events[1:]
				result.Dropped++
			}
		} else if strings.HasPrefix(e.Kind, "record.") && e.Kind != "record.ignored" {
			result.Diagnostics++
		}
	})
	if err != nil {
		http.Error(w, "History could not be read: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	result.Limited = limited
	writeJSON(w, result)
}

// Serve binds only to an explicit loopback IP. Port zero is useful in tests.
func Serve(ctx context.Context, address string, options Options, out io.Writer) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address must use a loopback IP, e.g. 127.0.0.1:7433")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	view, err := NewServer(options, listener.Addr().String())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); view.Watch(ctx) }()
	httpServer := &http.Server{Handler: view.Handler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024, BaseContext: func(net.Listener) context.Context { return ctx }}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = httpServer.Shutdown(shutdown)
	}()
	if _, err := url.Parse(view.URL()); err != nil {
		return err
	}
	fmt.Fprintf(out, "AgentView dashboard: %s\nObserving session files only. Ctrl-C stops AgentView.\n", view.URL())
	err = httpServer.Serve(listener)
	cancel()
	<-watchDone
	<-shutdownDone
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
