package config

import (
	"os"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

func Write(cfg *Config, path string) error {
	k := koanf.New(".")

	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		// if file does not exist, we can ignore the error
		if !os.IsNotExist(err) {
			return err
		}
	}

	if err := k.Set("telegram.mtproto.api_id", cfg.Telegram.MTProto.APIID); err != nil {
		return err
	}
	if err := k.Set("telegram.mtproto.api_hash", cfg.Telegram.MTProto.APIHash); err != nil {
		return err
	}
	if err := k.Set("telegram.mtproto.session_file", cfg.Telegram.MTProto.Session); err != nil {
		return err
	}
	if err := k.Set("proxy.socks5_addr", cfg.Proxy.SOCKS5Addr); err != nil {
		return err
	}

	b, err := k.Marshal(yaml.Parser())
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}
