package config

import (
	"log"
	"time"

	"github.com/spf13/viper"
)

// Config holds all application-wide configurations
type Config struct {
	DatabaseURL string
	RedisAddr   string
	RedisPassword string // Added
	RedisDB     int    // Added

	MinIO struct {
		Endpoint        string
		AccessKeyID     string
		SecretAccessKey string
		UseSSL          bool
		Buckets         struct {
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
	viper.AutomaticEnv() // read in environment variables that match
	viper.SetConfigFile(".env")

	if err = viper.ReadInConfig(); err != nil {
		log.Printf("Ignoring config file error: %v", err)
		err = nil // Clear the error so main.go doesn't fatally exit
	}

	// Explicitly get values from Viper, as Unmarshal might struggle with nested structs from flat env vars
	config.DatabaseURL = viper.GetString("SFSP_DATABASE_URL")
	config.RedisAddr = viper.GetString("REDIS_ADDR")
	config.RedisPassword = viper.GetString("REDIS_PASSWORD") // Added
	config.RedisDB = viper.GetInt("REDIS_DB")             // Added

	log.Printf("DEBUG: REDIS_ADDR read from Viper: [%s]", config.RedisAddr) // Debug log added

	config.MinIO.Endpoint = viper.GetString("SFSP_MINIO_ENDPOINT")
	config.MinIO.AccessKeyID = viper.GetString("SFSP_MINIO_ACCESS_KEY_ID")
	config.MinIO.SecretAccessKey = viper.GetString("SFSP_MINIO_SECRET_ACCESS_KEY")
	config.MinIO.UseSSL = viper.GetBool("SFSP_MINIO_USE_SSL")
	config.MinIO.Buckets.RawFiles = viper.GetString("SFSP_MINIO_BUCKET_RAW_FILES")
	config.MinIO.Buckets.CleanFiles = viper.GetString("SFSP_MINIO_BUCKET_CLEAN_FILES")
	config.MinIO.Buckets.Quarantine = viper.GetString("SFSP_MINIO_BUCKET_QUARANTINE")

	config.Scanner.ClamAVImage = viper.GetString("SFSP_SCANNER_CLAMAV_IMAGE")
	config.Scanner.YARAImage = viper.GetString("SFSP_SCANNER_YARA_IMAGE")
	config.Scanner.YARARulesPath = viper.GetString("SFSP_SCANNER_YARA_RULES_PATH")

	config.Worker.DockerHost = viper.GetString("DOCKER_HOST")

	config.API.Port = viper.GetString("SFSP_API_PORT")

	// Set default values if not provided by env or .env file
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

	return config, nil // Return nil for err as we handled the file not found case
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
