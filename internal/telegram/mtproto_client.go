package telegram

import (
	"os"
	"path/filepath"

	"github.com/gotd/log/logzap"
	"github.com/gotd/td/session"
	gotd "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"

	"go.uber.org/zap"

	"github.com/fmotalleb/rakhsh/config"
	"github.com/fmotalleb/rakhsh/internal/netx"
)

func newMTProtoClient(cfg *config.Config, logger *zap.Logger, updateHandler gotd.UpdateHandler) (*gotd.Client, error) {
	mt := cfg.Telegram.MTProto
	if err := os.MkdirAll(filepath.Dir(mt.Session), 0o750); err != nil {
		return nil, err
	}

	dialer := netx.NewSOCKS5Dialer(cfg.Proxy.SOCKS5Addr, cfg.Proxy.SOCKS5User, cfg.Proxy.SOCKS5Password)
	resolver := dcs.Plain(dcs.PlainOptions{
		Dial:       dcs.DialFunc(dialer),
		PreferIPv6: false,
	})
	originalDCS := dcs.Prod()
	ipv4DCS := new(dcs.List)
	for _, i := range originalDCS.Options {
		if !i.Ipv6 {
			ipv4DCS.Options = append(ipv4DCS.Options, i)
		}
	}
	ipv4DCS.Domains = originalDCS.Domains
	ipv4DCS.Test = false
	opts := gotd.Options{
		Logger:         logzap.New(logger),
		DCList:         *ipv4DCS,
		SessionStorage: &session.FileStorage{Path: mt.Session},
		Resolver:       resolver,
		UpdateHandler:  updateHandler,
		AllowCDN:       true,
		OnDead: func(err error) {
			logger.Error("Telegram client is dead", zap.Error(err))
		},
	}
	return gotd.NewClient(mt.APIID, mt.APIHash, opts), nil
}
