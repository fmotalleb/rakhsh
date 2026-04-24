package telegram

import (
	"context"
	"errors"
	"fmt"

	"github.com/fmotalleb/go-tools/log"
	"go.uber.org/zap"

	"github.com/fmotalleb/rakhsh/config"
)

func CreateMTProtoSession(ctx context.Context, cfg config.Config) error {
	logger := log.Of(ctx)
	cfg.ApplyDefaults()
	mt := cfg.Telegram.MTProto

	if mt.APIID <= 0 || mt.APIHash == "" {
		return errors.New("telegram.mtproto.api_id and telegram.mtproto.api_hash are required")
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

	return client.Run(ctx, func(runCtx context.Context) error {
		logger.Info("checking mtproto authorization state")
		status, statusErr := client.Auth().Status(runCtx)
		if statusErr != nil {
			return fmt.Errorf("check auth status: %w", statusErr)
		}
		if status.Authorized {
			userID := int64(0)
			if status.User != nil {
				userID = status.User.ID
			}
			logger.Info("mtproto session already authorized", zap.Int64("user_id", userID))
			return nil
		}

		if mt.Phone == "" {
			return errors.New("telegram.mtproto.phone is required for first-time login")
		}
		if mt.AuthCode == "" {
			return errors.New("telegram.mtproto.auth_code is required for first-time login")
		}

		logger.Info("performing mtproto login flow")
		if authErr := ensureMTProtoAuth(runCtx, client, mt); authErr != nil {
			return authErr
		}

		finalStatus, finalErr := client.Auth().Status(runCtx)
		if finalErr != nil {
			return fmt.Errorf("check final auth status: %w", finalErr)
		}
		if !finalStatus.Authorized {
			return errors.New("mtproto login finished but account is still unauthorized")
		}
		userID := int64(0)
		if finalStatus.User != nil {
			userID = finalStatus.User.ID
		}
		logger.Info("mtproto session created successfully",
			zap.String("session_file", mt.Session),
			zap.Int64("user_id", userID),
		)
		return nil
	})
}
