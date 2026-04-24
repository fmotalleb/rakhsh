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
		configFile, err := cmd.Flags().GetString("config")
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
		if err = config.Parse(ctx, &cfg, configFile); err != nil {
			return err
		}
		return telegram.CreateMTProtoSession(ctx, cfg)
	},
}
