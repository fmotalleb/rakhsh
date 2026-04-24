package config

import (
	"net/netip"
	"path/filepath"
	"time"
)

type Config struct {
	HTTP     HTTPConfig     `mapstructure:"http"`
	Telegram TelegramConfig `mapstructure:"telegram"`
	Proxy    ProxyConfig    `mapstructure:"proxy"`
	Download DownloadConfig `mapstructure:"download"`
}

type HTTPConfig struct {
	Listen    netip.AddrPort `mapstructure:"listen"`
	PublicURL string         `mapstructure:"public_url"`
	Storage   string         `mapstructure:"storage"`
}

type TelegramConfig struct {
	Mode           string        `mapstructure:"mode"`
	BotToken       string        `mapstructure:"bot_token"`
	UserToken      string        `mapstructure:"user_token"`
	AllowedUserIDs []int64       `mapstructure:"allowed_user_ids"`
	PollTimeout    int           `mapstructure:"poll_timeout"`
	UpdateInterval time.Duration `mapstructure:"update_interval"`

	MTProto MTProtoConfig `mapstructure:"mtproto"`
}

type MTProtoConfig struct {
	FallbackEnabled       bool   `mapstructure:"fallback_enabled"`
	FallbackForwardChatID int64  `mapstructure:"fallback_forward_chat_id"`
	APIID                 int    `mapstructure:"api_id"`
	APIHash               string `mapstructure:"api_hash"`
	Session               string `mapstructure:"session_file"`
	Phone                 string `mapstructure:"phone"`
	AuthCode              string `mapstructure:"auth_code"`
	Password              string `mapstructure:"password"`
	DeviceName            string `mapstructure:"device_name"`
}

type ProxyConfig struct {
	SOCKS5Addr     string `mapstructure:"socks5_addr"`
	SOCKS5User     string `mapstructure:"socks5_user"`
	SOCKS5Password string `mapstructure:"socks5_password"`
}

type DownloadConfig struct {
	MaxFileSize uint64        `mapstructure:"max_file_size"`
	MaxRetries  uint          `mapstructure:"max_retries"`
	RetryDelay  time.Duration `mapstructure:"retry_delay"`
}

func (c *Config) ApplyDefaults() {
	if !c.HTTP.Listen.IsValid() {
		c.HTTP.Listen = netip.MustParseAddrPort("0.0.0.0:8080")
	}
	if c.HTTP.Storage == "" {
		c.HTTP.Storage = filepath.Clean("./data")
	}
	if c.Telegram.Mode == "" {
		c.Telegram.Mode = "bot_api"
	}
	if c.Telegram.PollTimeout <= 0 {
		c.Telegram.PollTimeout = 30
	}
	if c.Telegram.UpdateInterval <= 0 {
		c.Telegram.UpdateInterval = 2 * time.Second
	}
	if c.Telegram.MTProto.Session == "" {
		c.Telegram.MTProto.Session = filepath.Clean("./data/mtproto.session")
	}
	if c.Telegram.MTProto.DeviceName == "" {
		c.Telegram.MTProto.DeviceName = "rakhsh"
	}
	if c.Download.MaxRetries == 0 {
		c.Download.MaxRetries = 5
	}
	if c.Download.RetryDelay <= 0 {
		c.Download.RetryDelay = 2 * time.Second
	}
}

func (c Config) TelegramAPIToken() string {
	if c.Telegram.BotToken != "" {
		return c.Telegram.BotToken
	}
	return c.Telegram.UserToken
}
