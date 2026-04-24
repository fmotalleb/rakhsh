package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"time"

	"github.com/fmotalleb/rakhsh/internal/storage"
)

type Server struct {
	httpServer *http.Server
}

func New(listen netip.AddrPort, storageRoot string) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/files/", func(w http.ResponseWriter, r *http.Request) {
		serveFile(w, r, storageRoot)
	})

	return &Server{
		httpServer: &http.Server{
			Addr:              listen.String(),
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
}

func (s *Server) Start(ctx context.Context) error {
	errChan := make(chan error, 1)
	go func() {
		err := s.httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errChan <- err
			return
		}
		errChan <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown http server: %w", err)
		}
		return ctx.Err()
	case err := <-errChan:
		return err
	}
}

func serveFile(w http.ResponseWriter, r *http.Request, storageRoot string) {
	name := strings.TrimPrefix(r.URL.Path, "/files/")
	name = filepath.Base(name)
	if name == "." || name == "" {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	hashFromName := storage.ExtractHashFromName(name)
	hashFromQuery := r.URL.Query().Get("h")
	if hashFromName == "" || hashFromQuery == "" || hashFromName != hashFromQuery {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	filePath := filepath.Join(storageRoot, name)
	http.ServeFile(w, r, filePath)
}
