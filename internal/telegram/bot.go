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

	"github.com/fmotalleb/go-tools/log"
	"go.uber.org/zap"

	"github.com/fmotalleb/rakhsh/config"
	"github.com/fmotalleb/rakhsh/internal/downloader"
	"github.com/fmotalleb/rakhsh/internal/storage"
)

var urlRegex = regexp.MustCompile(`https?://[^\s]+`)

type Handler struct {
	cfg        config.Config
	dl         *downloader.Downloader
	storage    *storage.Storage
	allowedIDs map[int64]struct{}
}

func NewHandler(cfg config.Config, dl *downloader.Downloader, store *storage.Storage) *Handler {
	allowed := make(map[int64]struct{}, len(cfg.Telegram.AllowedUserIDs))
	for _, id := range cfg.Telegram.AllowedUserIDs {
		allowed[id] = struct{}{}
	}
	return &Handler{cfg: cfg, dl: dl, storage: store, allowedIDs: allowed}
}

func (h *Handler) HandleMessage(ctx context.Context, api *API, message Message) {
	logger := log.Of(ctx)
	if message.From != nil && len(h.allowedIDs) > 0 {
		if _, ok := h.allowedIDs[message.From.ID]; !ok {
			_, _ = api.SendMessage(ctx, message.Chat.ID, "You are not allowed to use this bot", message.MessageID)
			return
		}
	}

	statusMessage, _ := api.SendMessage(ctx, message.Chat.ID, "Processing your request...", message.MessageID)

	fileName, sourceURL, err := h.resolveSource(ctx, api, message)
	if err != nil {
		_, _ = api.SendMessage(ctx, message.Chat.ID, "Please send a direct URL or attach a document", message.MessageID)
		logger.Debug("message ignored", zap.Error(err))
		return
	}

	tempPath := h.storage.TempPath(fileName)
	if removeErr := os.Remove(tempPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		logger.Warn("cleanup temp file failed", zap.Error(removeErr), zap.String("path", tempPath))
	}

	if err = h.dl.Download(ctx, sourceURL, tempPath); err != nil {
		_, _ = api.SendMessage(ctx, message.Chat.ID, fmt.Sprintf("Download failed: %v", err), message.MessageID)
		logger.Warn("download failed", zap.Error(err), zap.String("url", sourceURL))
		return
	}

	storedName, md5Hex, err := h.storage.Finalize(tempPath, fileName)
	if err != nil {
		_, _ = api.SendMessage(ctx, message.Chat.ID, fmt.Sprintf("Save failed: %v", err), message.MessageID)
		logger.Error("finalize failed", zap.Error(err))
		return
	}

	publicURL := strings.TrimRight(h.cfg.HTTP.PublicURL, "/") + "/files/" + url.PathEscape(storedName) + "?h=" + md5Hex
	_, _ = api.SendMessage(ctx, message.Chat.ID, "File ready: "+publicURL, statusMessage.MessageID)
}

func (h *Handler) resolveSource(ctx context.Context, api *API, message Message) (string, string, error) {
	if message.Document != nil {
		file, err := api.GetFile(ctx, message.Document.FileID)
		if err != nil {
			return "", "", err
		}
		name := message.Document.FileName
		if name == "" {
			name = filepath.Base(file.FilePath)
		}
		if name == "" {
			name = "telegram-file.bin"
		}
		return name, api.BuildFileDownloadURL(file.FilePath), nil
	}
	if message.Video != nil {
		file, err := api.GetFile(ctx, message.Video.FileID)
		if err != nil {
			return "", "", err
		}
		name := message.Video.FileName
		if name == "" {
			name = fallbackNameFromPath(file.FilePath, "telegram-video.mp4")
		}
		return name, api.BuildFileDownloadURL(file.FilePath), nil
	}
	if message.Audio != nil {
		file, err := api.GetFile(ctx, message.Audio.FileID)
		if err != nil {
			return "", "", err
		}
		name := message.Audio.FileName
		if name == "" {
			name = fallbackNameFromPath(file.FilePath, "telegram-audio.mp3")
		}
		return name, api.BuildFileDownloadURL(file.FilePath), nil
	}
	if message.Voice != nil {
		file, err := api.GetFile(ctx, message.Voice.FileID)
		if err != nil {
			return "", "", err
		}
		name := fallbackNameFromPath(file.FilePath, "telegram-voice.ogg")
		return name, api.BuildFileDownloadURL(file.FilePath), nil
	}
	if message.VideoNote != nil {
		file, err := api.GetFile(ctx, message.VideoNote.FileID)
		if err != nil {
			return "", "", err
		}
		name := fallbackNameFromPath(file.FilePath, "telegram-video-note.mp4")
		return name, api.BuildFileDownloadURL(file.FilePath), nil
	}
	if len(message.Photo) > 0 {
		photo := pickLargestPhoto(message.Photo)
		file, err := api.GetFile(ctx, photo.FileID)
		if err != nil {
			return "", "", err
		}
		name := fallbackNameFromPath(file.FilePath, "telegram-photo.jpg")
		return name, api.BuildFileDownloadURL(file.FilePath), nil
	}

	text := strings.TrimSpace(message.Text)
	if text == "" {
		return "", "", errors.New("empty message")
	}
	match := urlRegex.FindString(text)
	if match == "" {
		return "", "", errors.New("no url in message")
	}
	return downloader.ParseFilenameFromURL(match), match, nil
}

type Bot struct {
	api     *API
	handler *Handler
	cfg     config.Config
}

func NewBot(client *http.Client, cfg config.Config, handler *Handler) *Bot {
	token := cfg.TelegramAPIToken()
	return &Bot{api: NewAPI(client, token), handler: handler, cfg: cfg}
}

func (b *Bot) Run(ctx context.Context) error {
	if b.api.token == "" {
		return errors.New("telegram bot token (or user token fallback) is required")
	}
	logger := log.Of(ctx)
	offset := int64(0)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		updates, err := b.api.GetUpdates(ctx, offset, b.cfg.Telegram.PollTimeout)
		if err != nil {
			logger.Warn("getUpdates failed", zap.Error(err))
			continue
		}
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if update.Message == nil {
				continue
			}
			b.handler.HandleMessage(ctx, b.api, *update.Message)
		}
	}
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
	values := url.Values{}
	values.Set("offset", strconv.FormatInt(offset, 10))
	values.Set("timeout", strconv.Itoa(timeout))
	values.Set("allowed_updates", `["message"]`)

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

	var payload apiResponse[[]Update]
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode getUpdates response: %w", err)
	}
	if !payload.OK {
		return nil, fmt.Errorf("getUpdates failed: %s", payload.Description)
	}
	return payload.Result, nil
}

func (a *API) SendMessage(ctx context.Context, chatID int64, text string, replyTo int64) (Message, error) {
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("text", text)
	if replyTo > 0 {
		values.Set("reply_to_message_id", strconv.FormatInt(replyTo, 10))
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

	var payload apiResponse[Message]
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Message{}, err
	}
	if !payload.OK {
		return Message{}, fmt.Errorf("sendMessage failed: %s", payload.Description)
	}
	return payload.Result, nil
}

func (a *API) GetFile(ctx context.Context, fileID string) (File, error) {
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

	var payload apiResponse[File]
	if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return File{}, err
	}
	if !payload.OK {
		return File{}, fmt.Errorf("getFile failed: %s", payload.Description)
	}
	return payload.Result, nil
}

type apiResponse[T any] struct {
	OK          bool   `json:"ok"`
	Result      T      `json:"result"`
	Description string `json:"description"`
}

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64      `json:"message_id"`
	From      *User      `json:"from"`
	Chat      Chat       `json:"chat"`
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
