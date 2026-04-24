package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
		cfg.HTTP.PublicURL = "http://" + cfg.HTTP.Listen.String()
	}

	if err := storage.EnsureDir(cfg.HTTP.Storage); err != nil {
		return fmt.Errorf("create storage dir: %w", err)
	}

	proxyDialer := netx.NewSOCKS5Dialer(cfg.Proxy.SOCKS5Addr, cfg.Proxy.SOCKS5User, cfg.Proxy.SOCKS5Password)
	httpClient := netx.NewHTTPClient(proxyDialer)
	dl := downloader.New(httpClient, cfg.Download.MaxRetries, cfg.Download.RetryDelay, cfg.Download.MaxFileSize)
	store := storage.New(cfg.HTTP.Storage)

	httpSrv := httpserver.New(cfg.HTTP.Listen, cfg.HTTP.Storage)

	group, groupCtx := errgroup.WithContext(ctx)
	logger.Debug("application runtime configuration",
		zap.String("telegram_mode", strings.ToLower(cfg.Telegram.Mode)),
		zap.Bool("mtproto_fallback_enabled", cfg.Telegram.MTProto.FallbackEnabled),
		zap.String("storage_path", cfg.HTTP.Storage),
	)
	group.Go(func() error {
		logger.Info("http server started", zap.String("listen", cfg.HTTP.Listen.String()))
		return httpSrv.Start(groupCtx)
	})

	switch strings.ToLower(cfg.Telegram.Mode) {
	case "mtproto_user":
		userBot := telegram.NewMTProtoUserBot(cfg, dl, store)
		group.Go(func() error {
			logger.Info("mtproto userbot started")
			return userBot.Run(groupCtx)
		})
	default:
		var mtprotoFallback *telegram.MTProtoFallback
		if cfg.Telegram.MTProto.FallbackEnabled {
			mtprotoFallback = telegram.NewMTProtoFallback(cfg)
			group.Go(func() error {
				logger.Info("mtproto fallback started")
				return mtprotoFallback.Run(groupCtx)
			})
		}
		handler := telegram.NewHandler(cfg, dl, store, mtprotoFallback)
		bot := telegram.NewBot(httpClient, cfg, handler)
		group.Go(func() error {
			logger.Info("telegram bot api polling started")
			return bot.Run(groupCtx)
		})
	}

	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
