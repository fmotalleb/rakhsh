package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/fmotalleb/go-tools/git"
	"github.com/fmotalleb/go-tools/log"
	"github.com/spf13/cobra"

	"github.com/fmotalleb/rakhsh/app"
	"github.com/fmotalleb/rakhsh/config"
)

var debug bool

var rootCmd = &cobra.Command{
	Use:     "rakhsh",
	Short:   "Telegram fetch bot with direct file hosting",
	Version: git.String(),
	PersistentPreRun: func(_ *cobra.Command, _ []string) {
		if debug {
			log.SetDebugDefaults()
		}
	},
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

		return app.Run(ctx, cfg)
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().StringP("config", "c", "", "config file (default: reading config from stdin)")
	rootCmd.PersistentFlags().BoolVarP(&debug, "debug", "d", false, "enable debug mode")
}
