package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fmotalleb/go-tools/log"
	"github.com/gotd/td/session"
	gotd "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"github.com/fmotalleb/rakhsh/config"
	idownloader "github.com/fmotalleb/rakhsh/internal/downloader"
	"github.com/fmotalleb/rakhsh/internal/storage"
)

var mtprotoURLRegex = regexp.MustCompile(`https?://[^\s]+`)

type MTProtoUserBot struct {
	cfg     config.Config
	dl      *idownloader.Downloader
	storage *storage.Storage
}

func NewMTProtoUserBot(cfg config.Config, dl *idownloader.Downloader, store *storage.Storage) *MTProtoUserBot {
	return &MTProtoUserBot{cfg: cfg, dl: dl, storage: store}
}

func (b *MTProtoUserBot) Run(ctx context.Context) error {
	logger := log.Of(ctx)
	mt := b.cfg.Telegram.MTProto
	if mt.APIID <= 0 || mt.APIHash == "" {
		return errors.New("telegram.mtproto.api_id and telegram.mtproto.api_hash are required in mtproto_user mode")
	}
	if err := os.MkdirAll(filepath.Dir(mt.Session), 0o755); err != nil {
		return fmt.Errorf("create mtproto session dir: %w", err)
	}

	dispatcher := tg.NewUpdateDispatcher()
	client := gotd.NewClient(mt.APIID, mt.APIHash, gotd.Options{
		Logger:         logger,
		SessionStorage: &session.FileStorage{Path: mt.Session},
		UpdateHandler:  dispatcher,
	})

	return client.Run(ctx, func(runCtx context.Context) error {
		if err := ensureMTProtoAuth(runCtx, client, mt); err != nil {
			return err
		}

		sender := message.NewSender(tg.NewClient(client))
		download := downloader.NewDownloader()
		dispatcher.OnNewMessage(func(updateCtx context.Context, entities tg.Entities, update *tg.UpdateNewMessage) error {
			return b.handleNewMessage(updateCtx, client, sender, download, entities, update)
		})

		logger.Info("mtproto userbot started")
		<-runCtx.Done()
		return runCtx.Err()
	})
}

func (b *MTProtoUserBot) handleNewMessage(
	ctx context.Context,
	client *gotd.Client,
	sender *message.Sender,
	download *downloader.Downloader,
	entities tg.Entities,
	update *tg.UpdateNewMessage,
) error {
	logger := log.Of(ctx)
	msg, ok := update.Message.(*tg.Message)
	if !ok || msg.Out {
		return nil
	}

	fromID, hasFrom := msg.GetFromID()
	if hasFrom && len(b.cfg.Telegram.AllowedUserIDs) > 0 {
		peerUser, ok := fromID.(*tg.PeerUser)
		if !ok || !isAllowedUser(peerUser.UserID, b.cfg.Telegram.AllowedUserIDs) {
			_, _ = sender.Reply(entities, update).Text(ctx, "You are not allowed to use this bot")
			return nil
		}
	}

	fileName, directURL, location, err := b.resolveMTProtoSource(msg)
	if err != nil {
		return nil
	}

	tempPath := b.storage.TempPath(fileName)
	if removeErr := os.Remove(tempPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		logger.Warn("cleanup temp file failed", zap.Error(removeErr), zap.String("path", tempPath))
	}

	if directURL != "" {
		if err = b.dl.Download(ctx, directURL, tempPath); err != nil {
			_, _ = sender.Reply(entities, update).Text(ctx, fmt.Sprintf("Download failed: %v", err))
			return nil
		}
	} else {
		if _, err = download.Download(client.API(), location).ToPath(ctx, tempPath); err != nil {
			_, _ = sender.Reply(entities, update).Text(ctx, fmt.Sprintf("Telegram media download failed: %v", err))
			return nil
		}
	}

	storedName, md5Hex, err := b.storage.Finalize(tempPath, fileName)
	if err != nil {
		_, _ = sender.Reply(entities, update).Text(ctx, fmt.Sprintf("Save failed: %v", err))
		return nil
	}
	publicURL := strings.TrimRight(b.cfg.HTTP.PublicURL, "/") + "/files/" + url.PathEscape(storedName) + "?h=" + md5Hex
	_, _ = sender.Reply(entities, update).Text(ctx, "File ready: "+publicURL)
	return nil
}

func (b *MTProtoUserBot) resolveMTProtoSource(msg *tg.Message) (string, string, tg.InputFileLocationClass, error) {
	if txt := strings.TrimSpace(msg.Message); txt != "" {
		if match := mtprotoURLRegex.FindString(txt); match != "" {
			return idownloader.ParseFilenameFromURL(match), match, nil, nil
		}
	}

	media, ok := msg.GetMedia()
	if !ok {
		return "", "", nil, errors.New("no supported content")
	}
	switch m := media.(type) {
	case *tg.MessageMediaDocument:
		docClass, ok := m.GetDocument()
		if !ok {
			return "", "", nil, errors.New("document media missing document")
		}
		doc, ok := docClass.(*tg.Document)
		if !ok {
			return "", "", nil, errors.New("unsupported document type")
		}
		name := mtprotoDocumentName(doc)
		if name == "" {
			name = "telegram-file.bin"
		}
		return name, "", doc.AsInputDocumentFileLocation(), nil
	case *tg.MessageMediaPhoto:
		photoClass, ok := m.GetPhoto()
		if !ok {
			return "", "", nil, errors.New("photo media missing photo")
		}
		photo, ok := photoClass.(*tg.Photo)
		if !ok {
			return "", "", nil, errors.New("unsupported photo type")
		}
		thumb := pickBestPhotoThumb(photo.Sizes)
		location := &tg.InputPhotoFileLocation{
			ID:            photo.ID,
			AccessHash:    photo.AccessHash,
			FileReference: photo.FileReference,
			ThumbSize:     thumb,
		}
		return "telegram-photo.jpg", "", location, nil
	default:
		return "", "", nil, errors.New("unsupported media type")
	}
}

func mtprotoDocumentName(doc *tg.Document) string {
	for _, attr := range doc.Attributes {
		switch a := attr.(type) {
		case *tg.DocumentAttributeFilename:
			if strings.TrimSpace(a.FileName) != "" {
				return a.FileName
			}
		}
	}
	if strings.HasPrefix(strings.ToLower(doc.MimeType), "video/") {
		return "telegram-video.mp4"
	}
	if strings.HasPrefix(strings.ToLower(doc.MimeType), "audio/") {
		return "telegram-audio.mp3"
	}
	return "telegram-file.bin"
}

func isAllowedUser(id int64, allowed []int64) bool {
	for _, allowedID := range allowed {
		if allowedID == id {
			return true
		}
	}
	return false
}
