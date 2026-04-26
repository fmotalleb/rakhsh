package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/fmotalleb/go-tools/log"
	"github.com/spf13/cobra"

	"github.com/fmotalleb/rakhsh/config"
	"github.com/fmotalleb/rakhsh/internal/telegram"
)

var mtprotoSessionCmd = &cobra.Command{
	Use:   "mtproto-session",
	Short: "Create or verify MTProto user session file",
	RunE: func(cmd *cobra.Command, _ []string) error {
		log.SetDebugDefaults()
		configFile, err := cmd.Flags().GetString("config")
		if err != nil {
			return err
		}
		sessionFile, err := cmd.Flags().GetString("session")
		if err != nil {
			return err
		}
		apiID, err := cmd.Flags().GetInt("api-id")
		if err != nil {
			return err
		}
		apiHash, err := cmd.Flags().GetString("api-hash")
		if err != nil {
			return err
		}
		socks5, err := cmd.Flags().GetString("socks5")
		if err != nil {
			return err
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		ctx, err = log.WithNewEnvLogger(ctx)
		if err != nil {
			return err
		}

		var cfg config.Config
		if configFile != "" {
			if err = config.Parse(ctx, &cfg, configFile); err != nil {
				return err
			}
		}

		if sessionFile != "" {
			cfg.Telegram.MTProto.Session = sessionFile
		}
		if apiID != 0 {
			cfg.Telegram.MTProto.APIID = apiID
		}
		if apiHash != "" {
			cfg.Telegram.MTProto.APIHash = apiHash
		}
		if socks5 != "" {
			cfg.Proxy.SOCKS5Addr = socks5
		}

		return telegram.InteractiveMTProtoSession(ctx, &cfg, configFile)
	},
}

func init() {
	mtprotoSessionCmd.Flags().StringP("session", "s", "./secrets/mtproto.session", "Session file path")
	mtprotoSessionCmd.Flags().Int("api-id", 0, "API ID")
	mtprotoSessionCmd.Flags().String("api-hash", "", "API Hash")
	mtprotoSessionCmd.Flags().String("socks5", "", "SOCKS5 proxy address")
}
