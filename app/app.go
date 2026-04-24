package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/fmotalleb/go-tools/log"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/fmotalleb/rakhsh/config"
	"github.com/fmotalleb/rakhsh/internal/downloader"
	"github.com/fmotalleb/rakhsh/internal/httpserver"
	"github.com/fmotalleb/rakhsh/internal/netx"
	"github.com/fmotalleb/rakhsh/internal/storage"
	"github.com/fmotalleb/rakhsh/internal/telegram"
)

func Run(ctx context.Context, cfg config.Config) error {
	cfg.ApplyDefaults()
	logger := log.Of(ctx)

	if cfg.HTTP.PublicURL == "" {
		cfg.HTTP.PublicURL = fmt.Sprintf("http://%s", cfg.HTTP.Listen.String())
	}

	if err := storage.EnsureDir(cfg.HTTP.Storage); err != nil {
		return fmt.Errorf("create storage dir: %w", err)
	}

	proxyDialer := netx.NewSOCKS5Dialer(cfg.Proxy.SOCKS5Addr, cfg.Proxy.SOCKS5User, cfg.Proxy.SOCKS5Password)
	httpClient := netx.NewHTTPClient(proxyDialer)
	dl := downloader.New(httpClient, cfg.Download.MaxRetries, cfg.Download.RetryDelay, cfg.Download.MaxFileSize)
	store := storage.New(cfg.HTTP.Storage)
	handler := telegram.NewHandler(cfg, dl, store)

	httpSrv := httpserver.New(cfg.HTTP.Listen, cfg.HTTP.Storage)
	bot := telegram.NewBot(httpClient, cfg, handler)

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		logger.Info("http server started", zap.String("listen", cfg.HTTP.Listen.String()))
		return httpSrv.Start(groupCtx)
	})
	group.Go(func() error {
		logger.Info("telegram polling started")
		return bot.Run(groupCtx)
	})

	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
