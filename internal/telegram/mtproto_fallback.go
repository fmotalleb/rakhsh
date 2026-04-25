package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fmotalleb/go-tools/log"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"github.com/fmotalleb/rakhsh/config"
)

type MTProtoFallback struct {
	cfg config.Config

	ready chan struct{}
	once  sync.Once

	mu      sync.RWMutex
	api     *tg.Client
	peerMgr *peers.Manager
	dl      *downloader.Downloader
	selfID  int64
}

func NewMTProtoFallback(cfg config.Config) *MTProtoFallback {
	return &MTProtoFallback{cfg: cfg, ready: make(chan struct{})}
}

func (m *MTProtoFallback) Run(ctx context.Context) error {
	logger := log.Of(ctx)
	mt := m.cfg.Telegram.MTProto
	if mt.APIID <= 0 || mt.APIHash == "" {
		return errors.New("mtproto fallback requires telegram.mtproto.api_id and telegram.mtproto.api_hash")
	}
	logger.Debug("mtproto fallback starting",
		zap.Int("api_id", mt.APIID),
		zap.String("session_file", mt.Session),
		zap.String("socks5_addr", m.cfg.Proxy.SOCKS5Addr),
	)
	client, err := newMTProtoClient(m.cfg, logger, nil)
	if err != nil {
		return fmt.Errorf("init mtproto client: %w", err)
	}

	return client.Run(ctx, func(runCtx context.Context) error {
		status, err := client.Auth().Status(runCtx)
		if err != nil {
			return fmt.Errorf("mtproto auth status: %w", err)
		}
		if !status.Authorized {
			return errors.New("mtproto session not authorized")
		}

		peerMgr := (peers.Options{}).Build(tg.NewClient(client))
		if err := peerMgr.Init(runCtx); err != nil {
			return fmt.Errorf("init mtproto peer manager: %w", err)
		}

		m.mu.Lock()
		m.api = tg.NewClient(client)
		m.peerMgr = peerMgr
		m.dl = downloader.NewDownloader()
		status, _ = client.Auth().Status(runCtx)
		if status != nil && status.User != nil {
			m.selfID = status.User.ID
			logger.Debug("mtproto fallback authorized", zap.Int64("self_user_id", m.selfID))
		}
		m.mu.Unlock()

		m.once.Do(func() { close(m.ready) })
		<-runCtx.Done()
		return runCtx.Err()
	})
}

func (m *MTProtoFallback) IsSelfUser(userID int64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.selfID != 0 && m.selfID == userID
}

func (m *MTProtoFallback) DownloadFromBotMessage(
	ctx context.Context,
	botChatID int64,
	botMessageID int64,
	destination string,
	progressInterval time.Duration,
	progressCb func(downloaded int64, total int64),
) error {
	logger := log.Of(ctx)
	logger.Debug("mtproto fallback download requested",
		zap.Int64("bot_chat_id", botChatID),
		zap.Int64("bot_message_id", botMessageID),
		zap.String("destination", destination),
	)
	logger.Debug("waiting for mtproto fallback readiness")
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.ready:
	}
	logger.Debug("mtproto fallback ready")

	m.mu.RLock()
	api := m.api
	peerMgr := m.peerMgr
	dl := m.dl
	m.mu.RUnlock()
	if api == nil || peerMgr == nil || dl == nil {
		return errors.New("mtproto fallback not initialized")
	}

	inputPeer, inputChannel, err := resolveBotChatPeer(ctx, peerMgr, botChatID)
	if err != nil {
		return err
	}

	msg, err := fetchMessageByID(ctx, api, inputPeer, inputChannel, int(botMessageID))
	if err != nil {
		return err
	}
	location, err := mtprotoLocationFromMessage(msg)
	if err != nil {
		return err
	}
	totalSize := mtprotoMessageSize(msg)

	errChan := make(chan error, 1)
	go func() {
		_, downloadErr := dl.Download(api, location).ToPath(ctx, destination)
		errChan <- downloadErr
	}()

	if progressCb != nil {
		if progressInterval <= 0 {
			progressInterval = 2 * time.Second
		}
		ticker := time.NewTicker(progressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case downloadErr := <-errChan:
				size := fileSizeSafe(destination)
				progressCb(size, totalSize)
				if downloadErr != nil {
					return fmt.Errorf("mtproto fallback download: %w", downloadErr)
				}
				logger.Debug("mtproto fallback download completed", zap.String("destination", destination), zap.Int64("size", size))
				return nil
			case <-ticker.C:
				size := fileSizeSafe(destination)
				progressCb(size, totalSize)
				logger.Debug("mtproto fallback progress", zap.Int64("downloaded", size), zap.Int64("total", totalSize))
			}
		}
	}

	if downloadErr := <-errChan; downloadErr != nil {
		return fmt.Errorf("mtproto fallback download: %w", downloadErr)
	}
	logger.Debug("mtproto fallback download completed", zap.String("destination", destination))
	return nil
}

func resolveBotChatPeer(ctx context.Context, peerMgr *peers.Manager, botChatID int64) (tg.InputPeerClass, tg.InputChannelClass, error) {
	if botChatID > 0 {
		user, err := peerMgr.ResolveUserID(ctx, botChatID)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve user %d: %w", botChatID, err)
		}
		return user.InputPeer(), nil, nil
	}

	if channelID, ok := botChatToChannelID(botChatID); ok {
		channel, err := peerMgr.ResolveChannelID(ctx, channelID)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve channel %d: %w", channelID, err)
		}
		return channel.InputPeer(), channel.InputChannel(), nil
	}

	chatID := -botChatID
	chat, err := peerMgr.ResolveChatID(ctx, chatID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve chat %d: %w", chatID, err)
	}
	return chat.InputPeer(), nil, nil
}

func botChatToChannelID(chatID int64) (int64, bool) {
	s := strconv.FormatInt(chatID, 10)
	if !strings.HasPrefix(s, "-100") {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(s, "-100"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func fetchMessageByID(
	ctx context.Context,
	api *tg.Client,
	inputPeer tg.InputPeerClass,
	inputChannel tg.InputChannelClass,
	messageID int,
) (*tg.Message, error) {
	logger := log.Of(ctx)
	if inputChannel != nil {
		res, err := api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: inputChannel,
			ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: messageID}},
		})
		if err != nil {
			return nil, fmt.Errorf("channels.getMessages: %w", err)
		}
		return pickMessageByID(extractMessages(res), messageID)
	}

	// For users/basic groups, try exact lookup first.
	res, err := api.MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: messageID}})
	if err == nil {
		if msg, pickErr := pickMessageByID(extractMessages(res), messageID); pickErr == nil {
			return msg, nil
		}
	}
	logger.Debug("messages.getMessages did not return target, falling back to history scan", zap.Int("message_id", messageID))

	res, err = api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:      inputPeer,
		OffsetID:  messageID + 1,
		AddOffset: -150,
		Limit:     300,
	})
	if err != nil {
		return nil, fmt.Errorf("messages.getHistory: %w", err)
	}
	if msg, pickErr := pickMessageByID(extractMessages(res), messageID); pickErr == nil {
		return msg, nil
	}

	// Last attempt: scan latest messages in peer.
	latest, latestErr := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:  inputPeer,
		Limit: 300,
	})
	if latestErr != nil {
		return nil, fmt.Errorf("message %d not found in mtproto history (latest scan error: %w)", messageID, latestErr)
	}
	return pickMessageByID(extractMessages(latest), messageID)
}

func extractMessages(res tg.MessagesMessagesClass) []tg.MessageClass {
	switch r := res.(type) {
	case *tg.MessagesMessages:
		return r.Messages
	case *tg.MessagesMessagesSlice:
		return r.Messages
	case *tg.MessagesChannelMessages:
		return r.Messages
	default:
		return nil
	}
}

func pickMessageByID(messages []tg.MessageClass, messageID int) (*tg.Message, error) {
	for _, m := range messages {
		msg, ok := m.(*tg.Message)
		if ok && msg.ID == messageID {
			return msg, nil
		}
	}
	return nil, fmt.Errorf("message %d not found in mtproto history", messageID)
}

func mtprotoLocationFromMessage(msg *tg.Message) (tg.InputFileLocationClass, error) {
	media, ok := msg.GetMedia()
	if !ok {
		return nil, errors.New("message has no media")
	}

	switch m := media.(type) {
	case *tg.MessageMediaDocument:
		docClass, ok := m.GetDocument()
		if !ok {
			return nil, errors.New("document media missing document")
		}
		doc, ok := docClass.(*tg.Document)
		if !ok {
			return nil, errors.New("unsupported document type")
		}
		return doc.AsInputDocumentFileLocation(), nil
	case *tg.MessageMediaPhoto:
		photoClass, ok := m.GetPhoto()
		if !ok {
			return nil, errors.New("photo media missing photo")
		}
		photo, ok := photoClass.(*tg.Photo)
		if !ok {
			return nil, errors.New("unsupported photo type")
		}
		thumb := pickBestPhotoThumb(photo.Sizes)
		return &tg.InputPhotoFileLocation{
			ID:            photo.ID,
			AccessHash:    photo.AccessHash,
			FileReference: photo.FileReference,
			ThumbSize:     thumb,
		}, nil
	default:
		return nil, errors.New("unsupported media type for mtproto fallback")
	}
}

func mtprotoMessageSize(msg *tg.Message) int64 {
	media, ok := msg.GetMedia()
	if !ok {
		return -1
	}
	switch m := media.(type) {
	case *tg.MessageMediaDocument:
		docClass, ok := m.GetDocument()
		if !ok {
			return -1
		}
		doc, ok := docClass.(*tg.Document)
		if !ok {
			return -1
		}
		return doc.Size
	default:
		return -1
	}
}

func fileSizeSafe(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
