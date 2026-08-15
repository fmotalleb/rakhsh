package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fmotalleb/go-tools/log"
	"go.uber.org/zap"

	"github.com/fmotalleb/rakhsh/config"
	"github.com/fmotalleb/rakhsh/internal/downloader"
	"github.com/fmotalleb/rakhsh/internal/helper"
	"github.com/fmotalleb/rakhsh/internal/storage"
)

var urlRegex = regexp.MustCompile(`https?://[^\s]+`)

type Handler struct {
	cfg             config.Config
	dl              *downloader.Downloader
	storage         *storage.Storage
	fallback        *MTProtoFallback
	allowedIDs      map[int64]struct{}
	downloadCancels map[string]context.CancelFunc
	editLimiter     *chatEditLimiter
	mu              sync.Mutex
}

func NewHandler(cfg config.Config, dl *downloader.Downloader, store *storage.Storage, fallback *MTProtoFallback) *Handler {
	allowed := make(map[int64]struct{}, len(cfg.Telegram.AllowedUserIDs))
	for _, id := range cfg.Telegram.AllowedUserIDs {
		allowed[id] = struct{}{}
	}
	return &Handler{
		cfg:             cfg,
		dl:              dl,
		storage:         store,
		fallback:        fallback,
		allowedIDs:      allowed,
		downloadCancels: make(map[string]context.CancelFunc),
		editLimiter:     newChatEditLimiter(time.Second),
	}
}

func (h *Handler) HandleCallbackQuery(ctx context.Context, api *API, q *CallbackQuery) {
	logger := log.Of(ctx)
	logger.Debug("callback query received",
		zap.String("callback_query_id", q.ID),
		zap.String("data", q.Data),
		zap.Int64("from_id", q.From.ID),
	)
	if strings.HasPrefix(q.Data, "cancel:") {
		cancelKey := strings.TrimPrefix(q.Data, "cancel:")
		h.mu.Lock()
		cancel, ok := h.downloadCancels[cancelKey]
		h.mu.Unlock()
		if ok {
			cancel()
			logger.Debug("download cancelled", zap.String("cancel_key", cancelKey))
			_ = api.AnswerCallbackQuery(ctx, q.ID, "Download cancelled.")
			if q.Message != nil {
				_ = api.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, "Download cancelled.", nil)
			}
		} else {
			logger.Warn("cancel key not found", zap.String("cancel_key", cancelKey))
			_ = api.AnswerCallbackQuery(ctx, q.ID, "Could not cancel download (maybe it is already finished).")
		}
	}
}

func (h *Handler) HandleMessage(ctx context.Context, api *API, message Message) {
	go h.handler(ctx, message, api)
}

func (h *Handler) handler(ctx context.Context, message Message, api *API) {
	logger := log.Of(ctx)
	logger.Debug("message received",
		zap.Int64("chat_id", message.Chat.ID),
		zap.Int64("message_id", message.MessageID),
		zap.Bool("has_text", strings.TrimSpace(message.Text) != ""),
		zap.Bool("has_document", message.Document != nil),
		zap.Bool("has_video", message.Video != nil),
		zap.Bool("has_audio", message.Audio != nil),
		zap.Bool("has_voice", message.Voice != nil),
		zap.Bool("has_video_note", message.VideoNote != nil),
		zap.Int("photo_sizes", len(message.Photo)),
	)

	text := strings.TrimSpace(message.Text)
	if text == "/ids" || text == "/whoami" {
		printIDs(ctx, message, api)
		return
	}

	hasAccess := checkAccess(ctx, message, h, logger, api)
	if hasAccess {
		return
	}
	cancelKey := fmt.Sprintf("%d:%d", message.Chat.ID, message.MessageID)
	statusMessage, _ := api.SendMessage(ctx, message.Chat.ID, "Processing your request...", message.MessageID, nil)
	statusUpdater := newStatusUpdater(ctx, api, message.Chat.ID, statusMessage.MessageID, &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{
			{
				{Text: "Cancel", CallbackData: "cancel:" + cancelKey},
			},
		},
	}, h.editLimiter)
	_ = statusUpdater.Update("Preparing download...")

	fileName, sourceURL, fileID, useMTProtoFallback, err := h.resolveSource(ctx, api, message)
	if err != nil {
		_ = statusUpdater.UpdateAndRemoveMarkup("Please send a direct URL or attach a document")
		logger.Debug("message ignored", zap.Error(err))
		return
	}
	logger.Debug("source resolved",
		zap.String("file_name", fileName),
		zap.String("source_url", sourceURL),
		zap.Bool("use_mtproto_fallback", useMTProtoFallback),
	)

	tempPath := h.storage.TempPath(fileName)
	if removeErr := os.Remove(tempPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		logger.Warn("cleanup temp file failed", zap.Error(removeErr), zap.String("path", tempPath))
	}
	downloadCtx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	h.downloadCancels[cancelKey] = cancel
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.downloadCancels, cancelKey)
		h.mu.Unlock()
		cancel()
	}()
	if useMTProtoFallback {
		if h.fallback == nil {
			_ = statusUpdater.UpdateAndRemoveMarkup("Download failed: mtproto fallback is not configured")
			return
		}
		fallbackChatID := message.Chat.ID
		fallbackMessageID := message.MessageID

		wasRelayed := false
		_ = statusUpdater.Update("Switching to MTProto fallback (relay)...")
		logger.Debug("mtproto fallback selected with relay forwarding",
			zap.Int64("from_chat_id", message.Chat.ID),
			zap.Int64("from_message_id", message.MessageID),
			zap.Int64("fallback_forward_chat_id", h.cfg.Telegram.MTProto.FallbackForwardChatID),
		)
		forwardedMessage, forwardErr := api.ForwardMessage(
			ctx,
			h.cfg.Telegram.MTProto.FallbackForwardChatID,
			message.Chat.ID,
			message.MessageID,
		)
		if forwardErr != nil || forwardedMessage.MessageID == 0 {
			logger.Warn("fallback forward failed, trying to send document", zap.Error(forwardErr))
			sentMessage, sendErr := api.SendDocument(ctx, h.cfg.Telegram.MTProto.FallbackForwardChatID, fileID)
			if sendErr != nil || sentMessage.MessageID == 0 {
				logger.Warn("fallback send document failed, giving up", zap.Error(sendErr))
				_ = statusUpdater.UpdateAndRemoveMarkup("Failed to relay message for MTProto fallback.")
				return
			}
			fallbackChatID = sentMessage.Chat.ID
			fallbackMessageID = sentMessage.MessageID
			wasRelayed = true
			logger.Debug("fallback send document succeeded",
				zap.Int64("sent_chat_id", fallbackChatID),
				zap.Int64("sent_message_id", fallbackMessageID),
			)
		} else {
			fallbackChatID = forwardedMessage.Chat.ID
			fallbackMessageID = forwardedMessage.MessageID
			wasRelayed = true
			logger.Debug("fallback forward succeeded",
				zap.Int64("forwarded_chat_id", fallbackChatID),
				zap.Int64("forwarded_message_id", fallbackMessageID),
			)
		}

		_ = statusUpdater.Update("MTProto download started...")
		if err = h.fallback.DownloadFromBotMessage(
			downloadCtx,
			fallbackChatID,
			fallbackMessageID,
			tempPath,
			h.cfg.Telegram.UpdateInterval,
			func(p Progress) {
				logger.Debug("mtproto progress",
					zap.Int64("downloaded", p.Downloaded),
					zap.Int64("total", p.Total),
					zap.Float64("current_speed", p.CurrentSpeed),
					zap.Float64("avg_speed", p.AvgSpeed),
					zap.Duration("elapsed", p.Elapsed),
					zap.Duration("eta", p.ETA),
				)
				_ = statusUpdater.Update(formatMTProtoProgress(p))
			},
		); err != nil {
			_ = statusUpdater.UpdateAndRemoveMarkup(fmt.Sprintf("Download failed: %v", err))
			logger.Warn("mtproto fallback failed",
				zap.Error(err),
				zap.Int64("source_chat_id", message.Chat.ID),
				zap.Int64("source_message_id", message.MessageID),
				zap.Int64("fallback_chat_id", fallbackChatID),
				zap.Int64("fallback_message_id", fallbackMessageID),
			)
			return
		}
		if wasRelayed {
			if deleteErr := api.DeleteMessage(ctx, fallbackChatID, fallbackMessageID); deleteErr != nil {
				logger.Warn("failed to delete forwarded message", zap.Error(deleteErr))
			}
		}
	} else {
		if err = h.dl.DownloadWithProgress(
			downloadCtx,
			sourceURL,
			tempPath,
			h.cfg.Telegram.UpdateInterval,
			func(progress downloader.Progress) {
				logger.Debug("download progress",
					zap.Int64("downloaded", progress.Downloaded),
					zap.Int64("total", progress.Total),
					zap.Uint("attempt", progress.Attempt),
				)
				_ = statusUpdater.Update(progress.String())
			},
		); err != nil {
			_ = statusUpdater.UpdateAndRemoveMarkup(fmt.Sprintf("Download failed: %v", err))
			logger.Warn("download failed", zap.Error(err), zap.String("url", sourceURL))
			return
		}
	}

	storedName, md5Hex, err := h.storage.Finalize(tempPath, fileName)
	if err != nil {
		_ = statusUpdater.UpdateAndRemoveMarkup(fmt.Sprintf("Save failed: %v", err))
		logger.Error("finalize failed", zap.Error(err))
		return
	}

	publicURL := strings.TrimRight(h.cfg.HTTP.PublicURL, "/") + "/files/" + url.PathEscape(storedName) + "?h=" + md5Hex
	logger.Debug("file finalized",
		zap.String("stored_name", storedName),
		zap.String("md5", md5Hex),
		zap.String("public_url", publicURL),
	)
	if err = api.sendMessageWithRetry(ctx, message.Chat.ID, "File ready: "+publicURL, message.MessageID); err != nil {
		logger.Error("failed to send url to user", zap.Error(err))
		_ = statusUpdater.UpdateAndRemoveMarkup("File ready: " + publicURL)
		return
	}
	if err = api.DeleteMessage(ctx, statusUpdater.chatID, statusUpdater.messageID); err != nil {
		logger.Error("failed to delete update status message", zap.Error(err))
	}
	return
}

func checkAccess(ctx context.Context, message Message, h *Handler, logger *zap.Logger, api *API) bool {
	if message.From != nil && len(h.allowedIDs) > 0 {
		if _, ok := h.allowedIDs[message.From.ID]; !ok {
			logger.Debug("message rejected by allow list", zap.Int64("from_user_id", message.From.ID))
			_, _ = api.SendMessage(ctx, message.Chat.ID, "You are not allowed to use this bot", message.MessageID, nil)
			return true
		}
	}
	return false
}

func printIDs(ctx context.Context, message Message, api *API) {
	fromID := int64(0)
	if message.From != nil {
		fromID = message.From.ID
	}
	body := fmt.Sprintf(`chat_id: %d
from_user_id: %d
message_id: %d`, message.Chat.ID, fromID, message.MessageID)
	_, _ = api.SendMessage(ctx, message.Chat.ID, body, message.MessageID, nil)
}

func (h *Handler) resolveSource(ctx context.Context, api *API, message Message) (string, string, string, bool, error) {
	logger := log.Of(ctx)
	if message.Document != nil {
		return downloadDocument(ctx, message, api, logger, h)
	}
	if message.Video != nil {
		return downloadVideo(ctx, message, api, logger, h)
	}
	if message.Audio != nil {
		return downloadAudio(ctx, message, api, logger, h)
	}
	if message.Voice != nil {
		return downloadVoice(ctx, api, message, logger, h)
	}
	if message.VideoNote != nil {
		return downloadVideoNote(ctx, api, message, logger, h)
	}
	if len(message.Photo) > 0 {
		return downloadPhotos(ctx, message, api, logger, h)
	}

	text := strings.TrimSpace(message.Text)
	if text == "" {
		return "", "", "", false, errors.New("empty message")
	}
	match := urlRegex.FindString(text)
	if match == "" {
		return "", "", "", false, errors.New("no url in message")
	}
	return downloader.ParseFilenameFromURL(match), match, "", false, nil
}

func downloadPhotos(ctx context.Context, message Message, api *API, logger *zap.Logger, h *Handler) (string, string, string, bool, error) {
	photo := pickLargestPhoto(message.Photo)
	file, err := api.GetFile(ctx, photo.FileID)
	if err != nil {
		logger.Debug("bot getFile failed for photo, considering fallback", zap.Error(err))
		if h.fallback != nil {
			return "telegram-photo.jpg", "", photo.FileID, true, nil
		}
		return "", "", "", false, err
	}
	name := fallbackNameFromPath(file.FilePath, "telegram-photo.jpg")
	return name, api.BuildFileDownloadURL(file.FilePath), photo.FileID, false, nil
}

func downloadVideoNote(ctx context.Context, api *API, message Message, logger *zap.Logger, h *Handler) (string, string, string, bool, error) {
	file, err := api.GetFile(ctx, message.VideoNote.FileID)
	if err != nil {
		logger.Debug("bot getFile failed for video note, considering fallback", zap.Error(err))
		if h.fallback != nil {
			return "telegram-video-note.mp4", "", message.VideoNote.FileID, true, nil
		}
		return "", "", "", false, err
	}
	name := fallbackNameFromPath(file.FilePath, "telegram-video-note.mp4")
	return name, api.BuildFileDownloadURL(file.FilePath), message.VideoNote.FileID, false, nil
}

func downloadVoice(ctx context.Context, api *API, message Message, logger *zap.Logger, h *Handler) (string, string, string, bool, error) {
	file, err := api.GetFile(ctx, message.Voice.FileID)
	if err != nil {
		logger.Debug("bot getFile failed for voice, considering fallback", zap.Error(err))
		if h.fallback != nil {
			return "telegram-voice.ogg", "", message.Voice.FileID, true, nil
		}
		return "", "", "", false, err
	}
	name := fallbackNameFromPath(file.FilePath, "telegram-voice.ogg")
	return name, api.BuildFileDownloadURL(file.FilePath), message.Voice.FileID, false, nil
}

func downloadAudio(ctx context.Context, message Message, api *API, logger *zap.Logger, h *Handler) (string, string, string, bool, error) {
	name := message.Audio.FileName
	if name == "" {
		name = "telegram-audio.mp3"
	}
	file, err := api.GetFile(ctx, message.Audio.FileID)
	if err != nil {
		logger.Debug("bot getFile failed for audio, considering fallback", zap.Error(err))
		if h.fallback != nil {
			return name, "", message.Audio.FileID, true, nil
		}
		return "", "", "", false, err
	}
	if name == "" {
		name = fallbackNameFromPath(file.FilePath, "telegram-audio.mp3")
	}
	return name, api.BuildFileDownloadURL(file.FilePath), message.Audio.FileID, false, nil
}

func downloadVideo(ctx context.Context, message Message, api *API, logger *zap.Logger, h *Handler) (string, string, string, bool, error) {
	name := message.Video.FileName
	if name == "" {
		name = "telegram-video.mp4"
	}
	file, err := api.GetFile(ctx, message.Video.FileID)
	if err != nil {
		logger.Debug("bot getFile failed for video, considering fallback", zap.Error(err))
		if h.fallback != nil {
			return name, "", message.Video.FileID, true, nil
		}
		return "", "", "", false, err
	}
	if name == "" {
		name = fallbackNameFromPath(file.FilePath, "telegram-video.mp4")
	}
	return name, api.BuildFileDownloadURL(file.FilePath), message.Video.FileID, false, nil
}

func downloadDocument(ctx context.Context, message Message, api *API, logger *zap.Logger, h *Handler) (string, string, string, bool, error) {
	name := message.Document.FileName
	const telegramFileBin = "telegram-file.bin"
	if name == "" {
		name = telegramFileBin
	}
	file, err := api.GetFile(ctx, message.Document.FileID)
	if err != nil {
		logger.Debug("bot getFile failed for document, considering fallback", zap.Error(err))
		if h.fallback != nil {
			return name, "", message.Document.FileID, true, nil
		}
		return "", "", "", false, err
	}
	if name == "" {
		name = filepath.Base(file.FilePath)
	}
	if name == "" {
		name = telegramFileBin
	}
	return name, api.BuildFileDownloadURL(file.FilePath), message.Document.FileID, false, nil
}

type Bot struct {
	api     *API
	handler *Handler
	cfg     config.Config
	started int64
}

func NewBot(client *http.Client, cfg config.Config, handler *Handler) *Bot {
	token := cfg.TelegramAPIToken()
	return &Bot{api: NewAPI(client, token), handler: handler, cfg: cfg, started: time.Now().Unix()}
}

func (b *Bot) Run(ctx context.Context) error {
	if b.api.token == "" {
		return errors.New("telegram bot token (or user token fallback) is required")
	}
	logger := log.Of(ctx)
	offset := int64(0)
	offset = b.dropPendingUpdates(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		updates, err := b.api.GetUpdates(ctx, offset, b.cfg.Telegram.PollTimeout)
		if err != nil {
			logger.Debug("getUpdates failed", zap.Error(err))
			continue
		}
		logger.Debug("updates received", zap.Int("count", len(updates)), zap.Int64("offset", offset))
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if update.Message != nil {
				if update.Message.Date > 0 && int64(update.Message.Date) < b.started {
					logger.Debug("ignoring old message", zap.Int64("message_date", int64(update.Message.Date)), zap.Int64("started", b.started))
					continue
				}
				b.handler.HandleMessage(ctx, b.api, *update.Message)
			} else if update.CallbackQuery != nil {
				b.handler.HandleCallbackQuery(ctx, b.api, update.CallbackQuery)
			}
		}
	}
}

func (b *Bot) dropPendingUpdates(ctx context.Context) int64 {
	logger := log.Of(ctx)
	var offset int64
	for i := 0; i < 10; i++ {
		updates, err := b.api.GetUpdatesWithLimit(ctx, offset, 0, 100)
		if err != nil {
			logger.Warn("failed to drop pending updates, continuing with normal polling", zap.Error(err))
			return offset
		}
		if len(updates) == 0 {
			if offset > 0 {
				logger.Debug("dropped pending updates on startup", zap.Int64("offset", offset))
			}
			return offset
		}
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
		}
	}
	logger.Debug("startup drain hit max rounds", zap.Int64("offset", offset))
	return offset
}

type API struct {
	client  *http.Client
	token   string
	baseURL string
}

func NewAPI(client *http.Client, token string) *API {
	return &API{client: client, token: token, baseURL: "https://api.telegram.org"}
}

func (a *API) BuildFileDownloadURL(filePath string) string {
	return fmt.Sprintf("%s/file/bot%s/%s", a.baseURL, a.token, strings.TrimLeft(filePath, "/"))
}

func (a *API) endpoint(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", a.baseURL, a.token, method)
}

func (a *API) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	return a.GetUpdatesWithLimit(ctx, offset, timeout, 0)
}

func (a *API) GetUpdatesWithLimit(ctx context.Context, offset int64, timeout int, limit int) ([]Update, error) {
	log.Of(ctx).Debug("telegram api request",
		zap.String("method", "getUpdates"),
		zap.Int64("offset", offset),
		zap.Int("timeout", timeout),
		zap.Int("limit", limit),
	)
	values := url.Values{}
	values.Set("offset", strconv.FormatInt(offset, 10))
	values.Set("timeout", strconv.Itoa(timeout))
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	values.Set("allowed_updates", `["message","callback_query"]`)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint("getUpdates"), strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create getUpdates request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("getUpdates request failed: %w", err)
	}
	defer resp.Body.Close()
	log.Of(ctx).Debug("telegram api response", zap.String("method", "getUpdates"), zap.Int("status_code", resp.StatusCode))

	var payload apiResponse[[]Update]
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode getUpdates response: %w", err)
	}
	if !payload.OK {
		return nil, fmt.Errorf("getUpdates failed: %s", payload.Description)
	}
	return payload.Result, nil
}

func (a *API) SendMessage(ctx context.Context, chatID int64, text string, replyTo int64, replyMarkup *InlineKeyboardMarkup) (Message, error) {
	log.Of(ctx).Debug("telegram api request",
		zap.String("method", "sendMessage"),
		zap.Int64("chat_id", chatID),
		zap.Int64("reply_to_message_id", replyTo),
		zap.Int("text_length", len(text)),
	)
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("text", text)
	if replyTo > 0 {
		values.Set("reply_to_message_id", strconv.FormatInt(replyTo, 10))
	}
	if replyMarkup != nil {
		markupBytes, err := json.Marshal(replyMarkup)
		if err != nil {
			return Message{}, fmt.Errorf("marshal reply markup: %w", err)
		}
		values.Set("reply_markup", string(markupBytes))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint("sendMessage"), strings.NewReader(values.Encode()))
	if err != nil {
		return Message{}, fmt.Errorf("create sendMessage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	log.Of(ctx).Debug("telegram api response", zap.String("method", "sendMessage"), zap.Int("status_code", resp.StatusCode))

	var payload apiResponse[Message]
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Message{}, err
	}
	if !payload.OK {
		return Message{}, &apiError{
			method:      "sendMessage",
			description: payload.Description,
			retryAfter:  payload.retryAfter(),
		}
	}
	return payload.Result, nil
}

// maxSendRetries bounds how many times sendMessageWithRetry retries a message.
const maxSendRetries = 5

// sendMessageWithRetry sends text and retries on Telegram flood control
// (HTTP 429, honoring retry_after) and on transient errors with backoff. The
// final URL notification is the only chance the user gets the link, so it must
// not be dropped silently like regular progress edits.
func (a *API) sendMessageWithRetry(ctx context.Context, chatID int64, text string, replyTo int64) error {
	logger := log.Of(ctx)
	var lastErr error
	for attempt := 0; attempt < maxSendRetries; attempt++ {
		_, err := a.SendMessage(ctx, chatID, text, replyTo, nil)
		if err == nil {
			return nil
		}
		lastErr = err
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.retryAfter > 0 {
			delay := time.Duration(apiErr.retryAfter) * time.Second
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			delay += time.Duration(time.Now().UnixNano() % int64(time.Second))
			logger.Warn("telegram rate limited while sending url, waiting",
				zap.Int("retry_after", apiErr.retryAfter),
				zap.Int("attempt", attempt+1),
			)
			if sleepErr := sleepContext(ctx, delay); sleepErr != nil {
				return sleepErr
			}
			continue
		}
		delay := time.Duration(1<<uint(attempt)) * time.Second
		logger.Warn("failed to send url, retrying",
			zap.Error(err),
			zap.Int("attempt", attempt+1),
		)
		if sleepErr := sleepContext(ctx, delay); sleepErr != nil {
			return sleepErr
		}
	}
	return lastErr
}

func (a *API) GetFile(ctx context.Context, fileID string) (File, error) {
	log.Of(ctx).Debug("telegram api request",
		zap.String("method", "getFile"),
		zap.String("file_id", fileID),
	)
	values := url.Values{}
	values.Set("file_id", fileID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint("getFile"), strings.NewReader(values.Encode()))
	if err != nil {
		return File{}, fmt.Errorf("create getFile request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return File{}, err
	}
	defer resp.Body.Close()
	log.Of(ctx).Debug("telegram api response", zap.String("method", "getFile"), zap.Int("status_code", resp.StatusCode))

	var payload apiResponse[File]
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return File{}, err
	}
	if !payload.OK {
		return File{}, fmt.Errorf("getFile failed: %s", payload.Description)
	}
	return payload.Result, nil
}

func (a *API) ForwardMessage(ctx context.Context, toChatID int64, fromChatID int64, messageID int64) (Message, error) {
	log.Of(ctx).Debug("telegram api request",
		zap.String("method", "forwardMessage"),
		zap.Int64("to_chat_id", toChatID),
		zap.Int64("from_chat_id", fromChatID),
		zap.Int64("message_id", messageID),
	)
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(toChatID, 10))
	values.Set("from_chat_id", strconv.FormatInt(fromChatID, 10))
	values.Set("message_id", strconv.FormatInt(messageID, 10))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint("forwardMessage"), strings.NewReader(values.Encode()))
	if err != nil {
		return Message{}, fmt.Errorf("create forwardMessage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	log.Of(ctx).Debug("telegram api response", zap.String("method", "forwardMessage"), zap.Int("status_code", resp.StatusCode))

	var payload apiResponse[Message]
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Message{}, err
	}
	if !payload.OK {
		return Message{}, fmt.Errorf("forwardMessage failed: %s", payload.Description)
	}
	return payload.Result, nil
}

func (a *API) SendDocument(ctx context.Context, chatID int64, fileID string) (Message, error) {
	log.Of(ctx).Debug("telegram api request",
		zap.String("method", "sendDocument"),
		zap.Int64("chat_id", chatID),
		zap.String("file_id", fileID),
	)
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("document", fileID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint("sendDocument"), strings.NewReader(values.Encode()))
	if err != nil {
		return Message{}, fmt.Errorf("create sendDocument request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	log.Of(ctx).Debug("telegram api response", zap.String("method", "sendDocument"), zap.Int("status_code", resp.StatusCode))

	var payload apiResponse[Message]
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Message{}, err
	}
	if !payload.OK {
		return Message{}, fmt.Errorf("sendDocument failed: %s", payload.Description)
	}
	return payload.Result, nil
}

func (a *API) DeleteMessage(ctx context.Context, chatID int64, messageID int64) error {
	log.Of(ctx).Debug("telegram api request",
		zap.String("method", "deleteMessage"),
		zap.Int64("chat_id", chatID),
		zap.Int64("message_id", messageID),
	)
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("message_id", strconv.FormatInt(messageID, 10))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint("deleteMessage"), strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("create deleteMessage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	log.Of(ctx).Debug("telegram api response", zap.String("method", "deleteMessage"), zap.Int("status_code", resp.StatusCode))

	var payload apiResponse[json.RawMessage]
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	if !payload.OK {
		return fmt.Errorf("deleteMessage failed: %s", payload.Description)
	}
	return nil
}

func (a *API) AnswerCallbackQuery(ctx context.Context, callbackQueryID string, text string) error {
	log.Of(ctx).Debug("telegram api request",
		zap.String("method", "answerCallbackQuery"),
		zap.String("callback_query_id", callbackQueryID),
		zap.String("text", text),
	)
	values := url.Values{}
	values.Set("callback_query_id", callbackQueryID)
	values.Set("text", text)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint("answerCallbackQuery"), strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("create answerCallbackQuery request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	log.Of(ctx).Debug("telegram api response", zap.String("method", "answerCallbackQuery"), zap.Int("status_code", resp.StatusCode))

	var payload apiResponse[json.RawMessage]
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	if !payload.OK {
		return fmt.Errorf("answerCallbackQuery failed: %s", payload.Description)
	}
	return nil
}

func (a *API) EditMessageText(ctx context.Context, chatID int64, messageID int64, text string, replyMarkup *InlineKeyboardMarkup) error {
	log.Of(ctx).Debug("telegram api request",
		zap.String("method", "editMessageText"),
		zap.Int64("chat_id", chatID),
		zap.Int64("message_id", messageID),
		zap.Int("text_length", len(text)),
	)
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("message_id", strconv.FormatInt(messageID, 10))
	values.Set("text", text)
	if replyMarkup != nil {
		markupBytes, err := json.Marshal(replyMarkup)
		if err != nil {
			return fmt.Errorf("marshal reply markup: %w", err)
		}
		values.Set("reply_markup", string(markupBytes))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint("editMessageText"), strings.NewReader(values.Encode()))
	if err != nil {
		return fmt.Errorf("create editMessageText request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	log.Of(ctx).Debug("telegram api response", zap.String("method", "editMessageText"), zap.Int("status_code", resp.StatusCode))

	var payload apiResponse[json.RawMessage]
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	if !payload.OK {
		return fmt.Errorf("editMessageText failed: %s", payload.Description)
	}
	return nil
}

type apiResponse[T any] struct {
	OK          bool                `json:"ok"`
	Result      T                   `json:"result"`
	Description string              `json:"description"`
	Parameters  *responseParameters `json:"parameters"`
}

func (r apiResponse[T]) retryAfter() int {
	if r.Parameters != nil {
		return r.Parameters.RetryAfter
	}
	return 0
}

type responseParameters struct {
	RetryAfter int `json:"retry_after"`
}

// apiError is a Telegram Bot API error carrying the method name and, when the
// API responds with HTTP 429, the suggested retry delay.
type apiError struct {
	method      string
	description string
	retryAfter  int
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s failed: %s", e.method, e.description)
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type Message struct {
	MessageID int64      `json:"message_id"`
	From      *User      `json:"from"`
	Chat      Chat       `json:"chat"`
	Date      int        `json:"date"`
	Text      string     `json:"text"`
	Document  *Document  `json:"document"`
	Photo     []Photo    `json:"photo"`
	Video     *Video     `json:"video"`
	VideoNote *VideoNote `json:"video_note"`
	Audio     *Audio     `json:"audio"`
	Voice     *Voice     `json:"voice"`
}

type User struct {
	ID int64 `json:"id"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
}

type Photo struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type Video struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
}

type VideoNote struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
}

type Audio struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
}

type Voice struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
}

type File struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path"`
	FileSize int64  `json:"file_size"`
}

func pickLargestPhoto(photos []Photo) Photo {
	best := photos[0]
	for i := 1; i < len(photos); i++ {
		candidate := photos[i]
		if candidate.FileSize > best.FileSize {
			best = candidate
			continue
		}
		if candidate.FileSize == best.FileSize && candidate.Width*candidate.Height > best.Width*best.Height {
			best = candidate
		}
	}
	return best
}

func fallbackNameFromPath(filePath, fallback string) string {
	name := filepath.Base(filePath)
	if name == "" || name == "." || name == "/" {
		return fallback
	}
	return name
}

type statusUpdater struct {
	ctx         context.Context
	api         *API
	chatID      int64
	messageID   int64
	mu          sync.Mutex
	lastText    string
	replyMarkup *InlineKeyboardMarkup
	limiter     *chatEditLimiter
}

func newStatusUpdater(ctx context.Context, api *API, chatID int64, messageID int64, replyMarkup *InlineKeyboardMarkup, limiter *chatEditLimiter) *statusUpdater {
	return &statusUpdater{
		ctx:         ctx,
		api:         api,
		chatID:      chatID,
		messageID:   messageID,
		replyMarkup: replyMarkup,
		limiter:     limiter,
	}
}

func (u *statusUpdater) Update(text string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if text == "" || text == u.lastText {
		return nil
	}
	if !u.limiter.allow(u.chatID) {
		return nil
	}
	if err := u.api.EditMessageText(u.ctx, u.chatID, u.messageID, text, u.replyMarkup); err != nil {
		return err
	}
	u.lastText = text
	return nil
}

func (u *statusUpdater) UpdateAndRemoveMarkup(text string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if text == "" || text == u.lastText {
		return nil
	}
	if err := u.api.EditMessageText(u.ctx, u.chatID, u.messageID, text, nil); err != nil {
		return err
	}
	u.lastText = text
	u.replyMarkup = nil
	return nil
}

func formatMTProtoProgress(p Progress) string {
	if p.Total > 0 {
		percent := float64(p.Downloaded) * 100 / float64(p.Total)
		return fmt.Sprintf(`MTProto downloading... %s / %s (%.1f%%)
Speed: %s/s
Elapsed: %s
ETA: %s`,
			helper.HumanBytes(p.Downloaded),
			helper.HumanBytes(p.Total),
			percent,
			helper.HumanBytes(int64(p.CurrentSpeed)),
			p.Elapsed.Truncate(time.Second),
			p.ETA.Truncate(time.Second),
		)
	}
	return fmt.Sprintf("MTProto downloading... %s %s/s, Elapsed: %s",
		helper.HumanBytes(p.Downloaded),
		helper.HumanBytes(int64(p.CurrentSpeed)),
		p.Elapsed.Truncate(time.Second),
	)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
