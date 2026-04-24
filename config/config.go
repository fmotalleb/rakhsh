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
	BotToken       string        `mapstructure:"bot_token"`
	UserToken      string        `mapstructure:"user_token"`
	AllowedUserIDs []int64       `mapstructure:"allowed_user_ids"`
	PollTimeout    int           `mapstructure:"poll_timeout"`
	UpdateInterval time.Duration `mapstructure:"update_interval"`
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
	if c.Telegram.PollTimeout <= 0 {
		c.Telegram.PollTimeout = 30
	}
	if c.Telegram.UpdateInterval <= 0 {
		c.Telegram.UpdateInterval = 10 * time.Second
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
