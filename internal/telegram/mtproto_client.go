package telegram

import (
	"os"
	"path/filepath"

	"github.com/gotd/td/session"
	gotd "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"go.uber.org/zap"

	"github.com/fmotalleb/rakhsh/config"
	"github.com/fmotalleb/rakhsh/internal/netx"
)

func newMTProtoClient(cfg config.Config, logger *zap.Logger, updateHandler gotd.UpdateHandler) (*gotd.Client, error) {
	mt := cfg.Telegram.MTProto
	if err := os.MkdirAll(filepath.Dir(mt.Session), 0o755); err != nil {
		return nil, err
	}

	dialer := netx.NewSOCKS5Dialer(cfg.Proxy.SOCKS5Addr, cfg.Proxy.SOCKS5User, cfg.Proxy.SOCKS5Password)
	resolver := dcs.Plain(dcs.PlainOptions{
		Dial: dcs.DialFunc(dialer),
	})

	opts := gotd.Options{
		Logger:         logger,
		SessionStorage: &session.FileStorage{Path: mt.Session},
		Resolver:       resolver,
		UpdateHandler:  updateHandler,
	}
	return gotd.NewClient(mt.APIID, mt.APIHash, opts), nil
}
