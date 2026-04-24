package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/gotd/td/session"
	gotd "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"

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
}

func NewMTProtoFallback(cfg config.Config) *MTProtoFallback {
	return &MTProtoFallback{cfg: cfg, ready: make(chan struct{})}
}

func (m *MTProtoFallback) Run(ctx context.Context) error {
	mt := m.cfg.Telegram.MTProto
	if mt.APIID <= 0 || mt.APIHash == "" {
		return errors.New("mtproto fallback requires telegram.mtproto.api_id and telegram.mtproto.api_hash")
	}
	if err := os.MkdirAll(filepath.Dir(mt.Session), 0o755); err != nil {
		return fmt.Errorf("create mtproto session dir: %w", err)
	}

	client := gotd.NewClient(mt.APIID, mt.APIHash, gotd.Options{
		SessionStorage: &session.FileStorage{Path: mt.Session},
	})

	return client.Run(ctx, func(runCtx context.Context) error {
		if err := ensureMTProtoAuth(runCtx, client, mt); err != nil {
			return err
		}

		peerMgr := (peers.Options{}).Build(tg.NewClient(client))
		if err := peerMgr.Init(runCtx); err != nil {
			return fmt.Errorf("init mtproto peer manager: %w", err)
		}

		m.mu.Lock()
		m.api = tg.NewClient(client)
		m.peerMgr = peerMgr
		m.dl = downloader.NewDownloader()
		m.mu.Unlock()

		m.once.Do(func() { close(m.ready) })
		<-runCtx.Done()
		return runCtx.Err()
	})
}

func (m *MTProtoFallback) DownloadFromBotMessage(ctx context.Context, botChatID int64, botMessageID int64, destination string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.ready:
	}

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

	if _, err = dl.Download(api, location).ToPath(ctx, destination); err != nil {
		return fmt.Errorf("mtproto fallback download: %w", err)
	}
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

	res, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:      inputPeer,
		OffsetID:  messageID + 1,
		AddOffset: -10,
		Limit:     20,
	})
	if err != nil {
		return nil, fmt.Errorf("messages.getHistory: %w", err)
	}
	return pickMessageByID(extractMessages(res), messageID)
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

func ensureMTProtoAuth(ctx context.Context, client *gotd.Client, mt config.MTProtoConfig) error {
	status, err := client.Auth().Status(ctx)
	if err != nil {
		return fmt.Errorf("mtproto auth status: %w", err)
	}
	if status.Authorized {
		return nil
	}

	if mt.Phone == "" || mt.AuthCode == "" {
		return errors.New("mtproto not authorized; set telegram.mtproto.phone and telegram.mtproto.auth_code for initial login")
	}
	flow := auth.NewFlow(auth.CodeOnly(mt.Phone, auth.CodeAuthenticatorFunc(func(context.Context, *tg.AuthSentCode) (string, error) {
		return mt.AuthCode, nil
	})), auth.SendCodeOptions{})
	if mt.Password != "" {
		flow = auth.NewFlow(auth.Constant(mt.Phone, mt.Password, auth.CodeAuthenticatorFunc(func(context.Context, *tg.AuthSentCode) (string, error) {
			return mt.AuthCode, nil
		})), auth.SendCodeOptions{})
	}
	if err = client.Auth().IfNecessary(ctx, flow); err != nil {
		return fmt.Errorf("mtproto login: %w", err)
	}
	return nil
}
