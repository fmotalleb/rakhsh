package telegram

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/fmotalleb/go-tools/log"
	"github.com/gotd/td/telegram/auth"
	"go.uber.org/zap"

	"github.com/fmotalleb/rakhsh/config"
)

func InteractiveMTProtoSession(ctx context.Context, cfg *config.Config, configPath string) error {
	logger := log.Of(ctx)
	cfg.ApplyDefaults()
	mt := &cfg.Telegram.MTProto

	if mt.APIID == 0 {
		fmt.Print("First you need to create an app in https://my.telegram.org/apps")
		fmt.Print("Enter API ID: ")
		apiIDStr, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return err
		}
		apiID, err := strconv.Atoi(strings.TrimSpace(apiIDStr))
		if err != nil {
			return errors.New("invalid api id")
		}
		mt.APIID = apiID
	}
	if mt.APIHash == "" {
		fmt.Print("Enter API Hash: ")
		apiHash, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return err
		}
		mt.APIHash = strings.TrimSpace(apiHash)
	}

	if configPath != "" {
		if err := config.Write(cfg, configPath); err != nil {
			logger.Warn("failed to write config", zap.Error(err))
		}
	}

	logger.Info("starting mtproto session creation",
		zap.Int("api_id", mt.APIID),
		zap.String("session_file", mt.Session),
		zap.String("socks5_addr", cfg.Proxy.SOCKS5Addr),
	)
	client, err := newMTProtoClient(cfg, logger, nil)
	if err != nil {
		return fmt.Errorf("init mtproto client: %w", err)
	}
	fmt.Print("Enter Phone Number: ")
	phone, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return err
	}
	flow := auth.NewFlow(
		NewInteractiveAuth(strings.TrimSpace(phone)),
		auth.SendCodeOptions{},
	)

	return client.Run(ctx, func(runCtx context.Context) error {
		if err := client.Auth().IfNecessary(runCtx, flow); err != nil {
			return err
		}
		status, err := client.Auth().Status(runCtx)
		if err != nil {
			return err
		}
		logger.Info("mtproto session created successfully",
			zap.String("session_file", mt.Session),
			zap.Int64("user_id", status.User.ID),
		)

		return nil
	})
}
