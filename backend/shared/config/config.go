// Package config は共有パッケージ（主に queue）が使う Redis 接続設定の読み込みと、
// 外部サービス接続用のリトライ処理を提供する。
package config

import (
	"errors"
	"log"
	"os"
	"time"

	"github.com/spf13/viper"
)

// Config は共有パッケージが使う設定値を保持する（queue.ConnectRedis が開く Redis 接続用）。
type Config struct {
	RedisAddr     string `mapstructure:"REDIS_ADDR"`
	RedisPassword string `mapstructure:"REDIS_PASSWORD"`
	RedisDB       int    `mapstructure:"REDIS_DB"`
}

// LoadConfig はカレントディレクトリの .env ファイルと環境変数から設定を読み込む。
// .env が存在しない場合は警告を出して環境変数とデフォルト値だけで続行する。
func LoadConfig() (config Config, err error) {
	viper.SetConfigFile(".env")
	viper.AutomaticEnv() // 名前が一致する環境変数を読み込む

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

	// デフォルト値を設定する。
	// AutomaticEnv だけでは viper がキーを認識しないため Unmarshal に環境変数が反映されない。
	// SetDefault でキーを登録しておくことで、環境変数の値も Unmarshal 対象になる。
	viper.SetDefault("REDIS_ADDR", "redis:6379") // REDIS_ADDR のデフォルト値を追加（環境変数がない場合やViperマッピング用）
	viper.SetDefault("REDIS_PASSWORD", "")       // REDIS_PASSWORD のバインド用デフォルト値
	viper.SetDefault("REDIS_DB", 0)               // RedisDB のデフォルト値を追加

	err = viper.Unmarshal(&config)
	return
}

// Retry は外部サービスへの接続処理 fn を最大 attempts 回まで試行する。
// 失敗するたびに sleep だけ待ち、待ち時間を2倍にして再帰的に再試行する（指数バックオフ）。
// すべて失敗した場合は最後のエラーを返す。
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