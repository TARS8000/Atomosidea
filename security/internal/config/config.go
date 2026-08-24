package config

import (
	"log"
	"time"

	"github.com/spf13/viper"
)

// Config holds all application-wide configurations
type Config struct {
	DatabaseURL   string
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	MinIO struct {
		// Raw MinIO (ダウンロード用)
		RawEndpoint        string
		RawAccessKeyID     string
		RawSecretAccessKey string

		// Clean MinIO (アップロード用)
		CleanEndpoint        string
		CleanAccessKeyID     string
		CleanSecretAccessKey string

		UseSSL  bool
		Buckets struct {
			RawFiles   string
			CleanFiles string
			Quarantine string
		}
	}

	Scanner struct {
		ClamAVImage   string
		YARAImage     string
		YARARulesPath string
	}

	Worker struct {
		DockerHost string
	}

	API struct {
		Port string
	}
}

// LoadConfig loads configuration from file or environment variables
func LoadConfig() (config Config, err error) {
	viper.AutomaticEnv()
	viper.SetConfigFile(".env")

	if err = viper.ReadInConfig(); err != nil {
		log.Printf("Ignoring config file error: %v", err)
		err = nil
	}

	config.DatabaseURL = viper.GetString("SFSP_DATABASE_URL")
	config.RedisAddr = viper.GetString("REDIS_ADDR")
	config.RedisPassword = viper.GetString("REDIS_PASSWORD")
	config.RedisDB = viper.GetInt("REDIS_DB")

	log.Printf("DEBUG: REDIS_ADDR read from Viper: [%s]", config.RedisAddr)

	// Raw MinIO 接続情報の読み込み
	config.MinIO.RawEndpoint = viper.GetString("SFSP_RAW_MINIO_ENDPOINT")
	config.MinIO.RawAccessKeyID = viper.GetString("SFSP_RAW_MINIO_ACCESS_KEY_ID")
	config.MinIO.RawSecretAccessKey = viper.GetString("SFSP_RAW_MINIO_SECRET_ACCESS_KEY")

	// Clean MinIO 接続情報の読み込み
	config.MinIO.CleanEndpoint = viper.GetString("SFSP_CLEAN_MINIO_ENDPOINT")
	config.MinIO.CleanAccessKeyID = viper.GetString("SFSP_CLEAN_MINIO_ACCESS_KEY_ID")
	config.MinIO.CleanSecretAccessKey = viper.GetString("SFSP_CLEAN_MINIO_SECRET_ACCESS_KEY")

	// 環境変数が設定されていない場合のフォールバック（旧 SFSP_MINIO_* から取得）
	if config.MinIO.RawEndpoint == "" {
		config.MinIO.RawEndpoint = viper.GetString("SFSP_MINIO_ENDPOINT")
	}
	if config.MinIO.RawAccessKeyID == "" {
		config.MinIO.RawAccessKeyID = viper.GetString("SFSP_MINIO_ACCESS_KEY_ID")
	}
	if config.MinIO.RawSecretAccessKey == "" {
		config.MinIO.RawSecretAccessKey = viper.GetString("SFSP_MINIO_SECRET_ACCESS_KEY")
	}

	config.MinIO.UseSSL = viper.GetBool("SFSP_MINIO_USE_SSL")
	config.MinIO.Buckets.RawFiles = viper.GetString("SFSP_MINIO_BUCKET_RAW_FILES")
	config.MinIO.Buckets.CleanFiles = viper.GetString("SFSP_MINIO_BUCKET_CLEAN_FILES")
	config.MinIO.Buckets.Quarantine = viper.GetString("SFSP_MINIO_BUCKET_QUARANTINE")

	config.Scanner.ClamAVImage = viper.GetString("SFSP_SCANNER_CLAMAV_IMAGE")
	config.Scanner.YARAImage = viper.GetString("SFSP_SCANNER_YARA_IMAGE")
	config.Scanner.YARARulesPath = viper.GetString("SFSP_SCANNER_YARA_RULES_PATH")

	config.Worker.DockerHost = viper.GetString("DOCKER_HOST")
	config.API.Port = viper.GetString("SFSP_API_PORT")

	// デフォルト値の設定
	if config.MinIO.Buckets.RawFiles == "" {
		config.MinIO.Buckets.RawFiles = "raw-files"
	}
	if config.MinIO.Buckets.CleanFiles == "" {
		config.MinIO.Buckets.CleanFiles = "clean-files"
	}
	if config.MinIO.Buckets.Quarantine == "" {
		config.MinIO.Buckets.Quarantine = "quarantine"
	}
	if config.API.Port == "" {
		config.API.Port = "8080"
	}
	if config.Scanner.ClamAVImage == "" {
		config.Scanner.ClamAVImage = "sfsp-clamav-client:latest"
	}
	if config.Scanner.YARAImage == "" {
		config.Scanner.YARAImage = "sfsp-yara-client:latest"
	}
	if config.Scanner.YARARulesPath == "" {
		config.Scanner.YARARulesPath = "/etc/yara-rules/general.yar"
	}

	return config, nil
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