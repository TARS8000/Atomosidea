package config

import (
    "log"
    "time"

    "github.com/spf13/viper"
)

// Config holds all application-wide configurations
type Config struct {
    DatabaseURL   string `mapstructure:"SFSP_DATABASE_URL"`
    RedisAddr     string `mapstructure:"REDIS_ADDR"`
    RedisPassword string `mapstructure:"REDIS_PASSWORD"` // RedisPassword を追加
    RedisDB       int    `mapstructure:"REDIS_DB"`       // RedisDB を追加

    MinIO struct {
       Endpoint        string `mapstructure:"SFSP_MINIO_ENDPOINT"`
       AccessKeyID     string `mapstructure:"SFSP_MINIO_ACCESS_KEY_ID"`
       SecretAccessKey string `mapstructure:"SFSP_MINIO_SECRET_ACCESS_KEY"`
       UseSSL          bool   `mapstructure:"SFSP_MINIO_USE_SSL"`
       Buckets         struct {
          RawFiles   string `mapstructure:"SFSP_MINIO_BUCKET_RAW_FILES"`
          CleanFiles string `mapstructure:"SFSP_MINIO_BUCKET_CLEAN_FILES"`
          Quarantine string `mapstructure:"SFSP_MINIO_BUCKET_QUARANTINE"`
       }
    }

    Scanner struct {
       ClamAVImage   string `mapstructure:"SFSP_SCANNER_CLAMAV_IMAGE"`
       YARAImage     string `mapstructure:"SFSP_SCANNER_YARA_IMAGE"`
       YARARulesPath string `mapstructure:"SFSP_SCANNER_YARA_RULES_PATH"`
    }

    Worker struct {
       DockerHost string `mapstructure:"DOCKER_HOST"`
    }

    API struct {
       Port string `mapstructure:"SFSP_API_PORT"`
    }
}

// LoadConfig loads configuration from file or environment variables
func LoadConfig() (config Config, err error) {
    viper.SetConfigFile(".env")
    viper.AutomaticEnv() // read in environment variables that match

    if err = viper.ReadInConfig(); err != nil {
       if _, ok := err.(viper.ConfigFileNotFoundError); ok {
          log.Println("Warning: .env file not found, using environment variables or defaults.")
       } else {
          return
       }
    }

    // Set default values
    viper.SetDefault("REDIS_ADDR", "redis:6379") // REDIS_ADDR のデフォルト値を追加（環境変数がない場合やViperマッピング用）
    viper.SetDefault("REDIS_PASSWORD", "")       // REDIS_PASSWORD のバインド用デフォルト値
    viper.SetDefault("REDIS_DB", 0)               // RedisDB のデフォルト値を追加
    viper.SetDefault("SFSP_MINIO_BUCKET_RAW_FILES", "raw-files")
    viper.SetDefault("SFSP_MINIO_BUCKET_CLEAN_FILES", "clean-files")
    viper.SetDefault("SFSP_MINIO_BUCKET_QUARANTINE", "quarantine")
    viper.SetDefault("SFSP_API_PORT", "8080")
    viper.SetDefault("SFSP_SCANNER_CLAMAV_IMAGE", "sfsp-clamav-client:latest")
    viper.SetDefault("SFSP_SCANNER_YARA_IMAGE", "sfsp-yara-client:latest")
    viper.SetDefault("SFSP_SCANNER_YARA_RULES_PATH", "/etc/yara-rules/general.yar")

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