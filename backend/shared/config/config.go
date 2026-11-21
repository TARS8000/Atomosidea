package config

import (
	"errors"
	"log"
	"os"
	"time"

	"github.com/spf13/viper"
)

// Config holds the settings used by the shared packages (the Redis connection opened by queue.ConnectRedis)
type Config struct {
	RedisAddr     string `mapstructure:"REDIS_ADDR"`
	RedisPassword string `mapstructure:"REDIS_PASSWORD"`
	RedisDB       int    `mapstructure:"REDIS_DB"`
}

// LoadConfig loads configuration from file or environment variables
func LoadConfig() (config Config, err error) {
	viper.SetConfigFile(".env")
	viper.AutomaticEnv() // read in environment variables that match

	if err = viper.ReadInConfig(); err != nil {
		var configFileNotFoundErr viper.ConfigFileNotFoundError
		// .env ファイルが存在しないエラー（viper固有エラー、またはos/fsレベルの非存在エラー）を検知して安全に無視する
		if errors.As(err, &configFileNotFoundErr) || os.IsNotExist(err) {
			log.Println("Warning: .env file not found, using environment variables or defaults.")
			err = nil // エラーを無視して処理を続行
		} else {
			return
		}
	}

	// Set default values
	viper.SetDefault("REDIS_ADDR", "redis:6379") // REDIS_ADDR のデフォルト値を追加（環境変数がない場合やViperマッピング用）
	viper.SetDefault("REDIS_PASSWORD", "")       // REDIS_PASSWORD のバインド用デフォルト値
	viper.SetDefault("REDIS_DB", 0)               // RedisDB のデフォルト値を追加

	err = viper.Unmarshal(&config)
	return
}

// Retry function for connecting to external services
func Retry(attempts int, sleep time.Duration, fn func() error) error {
	if err := fn(); err != nil {
		if attempts--; attempts > 0 {
			log.Printf("Retrying after error: %v. Attempts left: %d", err, attempts)
			time.Sleep(sleep)
			return Retry(attempts, 2*sleep, fn)
		}
		return err
	}
	return nil
}