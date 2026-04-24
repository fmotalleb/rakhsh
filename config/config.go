package config

import (
	"net/netip"
	"time"
)

type Config struct {
	HttpListen netip.AddrPort `mapstructure:"http_listen"`

	Telegram TelegramConfig `mapstructure:"telegram"`
	// File size limit
	MaxFileSize uint64 `mapstructure:"max_file_size"`

	// Max retry file (continue downloading file if failed using headers)
	MaxRetries uint `mapstructure:"max_retries"`

	// Send progress information to user
	UpdateInterval time.Duration `mapstructure:"update_interval"`
}

// Bot token, proxy, user token, user ids to answer to (or set answer_all: true), etc
type TelegramConfig struct {
}
