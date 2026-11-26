// Package config は SFSP（sfsp-api / sfsp-worker）の設定を環境変数から読み込み、
// ログ出力用に認証情報を伏せるヘルパーを提供する。
package config

import (
	"log"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// サイズ単位と、ClamAV が扱えるサイズの上限値。
const (
	MiB = int64(1024 * 1024)
	GiB = 1024 * MiB

	// ClamAV 1.x は 2 GiB - 1 バイトを超えるファイルを検査できない（それより大きい
	// --max-filesize を渡しても警告が出るだけで実際には検査されない）
	ClamAVMaxSupportedFileSize = int64(2147483647)
	// clamscan の --max-scansize の上限（4000M）
	ClamAVMaxSupportedScanSize = 4000 * MiB
)

// Config は SFSP 全体で使う設定値を保持する。
type Config struct {
	// sfsp-db への接続文字列（SFSP_DATABASE_URL）
	DatabaseURL string

	// MinIO の接続情報とバケット名
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

	// スキャナー用コンテナのイメージ名と YARA ルールファイルのパス
	Scanner struct {
		ClamAVImage   string
		YARAImage     string
		YARARulesPath string
	}

	// ClamAV に明示的に渡す上限値（clamscan の既定値は小さく、超えたファイルは
	// 「検査せずに clean」と扱われるため）。サイズはバイト。
	ClamAV struct {
		MaxFileSize     int64 // --max-filesize（SFSP_CLAMAV_MAX_FILESIZE）
		MaxScanSize     int64 // --max-scansize（SFSP_CLAMAV_MAX_SCANSIZE）
		MaxFiles        int   // --max-files（SFSP_CLAMAV_MAX_FILES）
		MaxRecursion    int   // --max-recursion（SFSP_CLAMAV_MAX_RECURSION）
		MaxDirRecursion int   // --max-dir-recursion（SFSP_CLAMAV_MAX_DIR_RECURSION）
		MaxScanTimeMs   int64 // --max-scantime（SFSP_CLAMAV_MAX_SCANTIME_MS）
		PCREMaxFileSize int64 // --pcre-max-filesize（SFSP_CLAMAV_PCRE_MAX_FILESIZE）
	}

	// アップロードと zip 展開の上限
	Limits struct {
		MaxUploadBytes    int64 // SFSP_MAX_UPLOAD_BYTES: sfsp-api が受け付ける 1 ファイルの上限
		MaxExtractedBytes int64 // SFSP_MAX_EXTRACTED_BYTES: zip 展開後の合計サイズ
		MaxZipEntries     int   // SFSP_MAX_ZIP_ENTRIES: zip のエントリ数
		MaxZipEntryBytes  int64 // SFSP_MAX_ZIP_ENTRY_BYTES: zip の 1 エントリの展開後サイズ
		MaxZipRatio       int64 // SFSP_MAX_ZIP_RATIO: 1 エントリの圧縮率（展開後 / 圧縮後）の上限
		MaxZipDepth       int   // SFSP_MAX_ZIP_DEPTH: zip 内のディレクトリ階層の上限
	}

	// sfsp-api の待ち受けポートと API キー
	API struct {
		Port string
		Key  string // SFSP_API_KEY: /api/v1/* に必須の X-SFSP-API-Key
	}
}

// LoadConfig は環境変数から設定を読み込み、未設定の項目にはデフォルト値を補う。
// カレントディレクトリに .env があれば補助的に読み込むが、無くても動作する（環境変数が優先）。
// 現状はエラーを返すことはない（不正な数値はログを出してデフォルト値を使う）。
func LoadConfig() (config Config, err error) {
	// 1. 環境変数と .env（存在する場合のみ）を読み込み元にする
	viper.AutomaticEnv()
	if _, statErr := os.Stat(".env"); statErr == nil {
		viper.SetConfigFile(".env")
		if readErr := viper.ReadInConfig(); readErr != nil {
			log.Printf("Ignoring .env read error: %v", readErr)
		}
	}

	// 2. 接続情報などの文字列設定を読み込む
	config.DatabaseURL = viper.GetString("SFSP_DATABASE_URL")

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

	config.API.Port = viper.GetString("SFSP_API_PORT")
	config.API.Key = strings.TrimSpace(viper.GetString("SFSP_API_KEY"))

	// 3. デフォルト値の設定
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

	// 4. ClamAV の上限（既定はサポートされる最大値）
	config.ClamAV.MaxFileSize = clampInt64("SFSP_CLAMAV_MAX_FILESIZE", getInt64("SFSP_CLAMAV_MAX_FILESIZE", ClamAVMaxSupportedFileSize), 1, ClamAVMaxSupportedFileSize)
	config.ClamAV.MaxScanSize = clampInt64("SFSP_CLAMAV_MAX_SCANSIZE", getInt64("SFSP_CLAMAV_MAX_SCANSIZE", ClamAVMaxSupportedScanSize), 1, ClamAVMaxSupportedScanSize)
	config.ClamAV.MaxFiles = int(getInt64("SFSP_CLAMAV_MAX_FILES", 100000))
	config.ClamAV.MaxRecursion = int(getInt64("SFSP_CLAMAV_MAX_RECURSION", 64))
	config.ClamAV.MaxDirRecursion = int(getInt64("SFSP_CLAMAV_MAX_DIR_RECURSION", 64))
	config.ClamAV.MaxScanTimeMs = getInt64("SFSP_CLAMAV_MAX_SCANTIME_MS", 25*60*1000) // サンドボックスの 30 分より短く
	config.ClamAV.PCREMaxFileSize = clampInt64("SFSP_CLAMAV_PCRE_MAX_FILESIZE", getInt64("SFSP_CLAMAV_PCRE_MAX_FILESIZE", ClamAVMaxSupportedFileSize), 1, ClamAVMaxSupportedFileSize)

	// 5. アップロード・展開の上限
	config.Limits.MaxUploadBytes = getInt64("SFSP_MAX_UPLOAD_BYTES", ClamAVMaxSupportedFileSize)
	config.Limits.MaxExtractedBytes = getInt64("SFSP_MAX_EXTRACTED_BYTES", 4*GiB)
	config.Limits.MaxZipEntries = int(getInt64("SFSP_MAX_ZIP_ENTRIES", 20000))
	// 1 エントリは ClamAV が検査できるサイズを超えないようにする
	config.Limits.MaxZipEntryBytes = clampInt64("SFSP_MAX_ZIP_ENTRY_BYTES", getInt64("SFSP_MAX_ZIP_ENTRY_BYTES", config.ClamAV.MaxFileSize), 1, config.ClamAV.MaxFileSize)
	config.Limits.MaxZipRatio = getInt64("SFSP_MAX_ZIP_RATIO", 1000)
	config.Limits.MaxZipDepth = int(getInt64("SFSP_MAX_ZIP_DEPTH", 32))
	// clamscan は --max-dir-recursion より深いファイルを警告なしで飛ばすため、zip の階層上限はそれより小さくする
	if maxDepth := config.ClamAV.MaxDirRecursion - 8; config.Limits.MaxZipDepth > maxDepth {
		log.Printf("SFSP_MAX_ZIP_DEPTH=%d is too deep for SFSP_CLAMAV_MAX_DIR_RECURSION=%d; using %d", config.Limits.MaxZipDepth, config.ClamAV.MaxDirRecursion, maxDepth)
		config.Limits.MaxZipDepth = maxDepth
	}
	if config.Limits.MaxZipDepth < 1 {
		config.Limits.MaxZipDepth = 1
	}

	return config, nil
}

// getInt64 は環境変数 key を int64 として読み込む。未設定なら def を返し、
// 数値として解釈できない値や 0 以下の値の場合はログを出して def を返す。
func getInt64(key string, def int64) int64 {
	s := strings.TrimSpace(viper.GetString(key))
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		log.Printf("Invalid value for %s (%q); using default %d", key, s, def)
		return def
	}
	return v
}

// clampInt64 は v を [min, max] の範囲に収めて返す。max を超えた場合のみ key 名付きでログを出す。
func clampInt64(key string, v, min, max int64) int64 {
	if v < min {
		return min
	}
	if v > max {
		log.Printf("%s=%d exceeds the supported maximum; using %d", key, v, max)
		return max
	}
	return v
}

// ResolveRedisAddr は使用する Redis の接続先を返す。環境変数 REDIS_URL（例: redis://:<pw>@redis:6379）が
// あれば addr より優先し、どちらも空なら "redis:6379" を使う。
// 結果がパスワードを含まない URL で、password が空でない場合は URL にパスワードを埋め込む。
// URL 形式では shared/queue が ParseURL を使い REDIS_PASSWORD を参照しないため、こうしないと認証できない。
func ResolveRedisAddr(addr, password string) string {
	if u := strings.TrimSpace(os.Getenv("REDIS_URL")); u != "" {
		addr = u
	}
	if addr == "" {
		addr = "redis:6379"
	}
	if password == "" || !(strings.HasPrefix(addr, "redis://") || strings.HasPrefix(addr, "rediss://")) {
		return addr
	}
	parsed, err := url.Parse(addr)
	if err != nil {
		return addr
	}
	if _, has := parsed.User.Password(); has {
		return addr
	}
	username := ""
	if parsed.User != nil {
		username = parsed.User.Username()
	}
	parsed.User = url.UserPassword(username, password)
	return parsed.String()
}

// dsnPasswordRe は key=value 形式の DSN から password=... の値部分（引用符付きも含む）を探す正規表現。
var dsnPasswordRe = regexp.MustCompile(`(?i)(password\s*=\s*)('[^']*'|\S+)`)

// RedactURL は DSN / URL に含まれる認証情報を伏せ、安全にログ出力できる形にする。
// URL 形式（scheme://user:pw@host）と key=value 形式（password=...）の両方に対応する。
func RedactURL(s string) string {
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			return u.Redacted()
		}
		// パースできない URL は userinfo を丸ごと隠す
		if i := strings.Index(s, "://"); i >= 0 {
			rest := s[i+3:]
			if at := strings.LastIndex(rest, "@"); at >= 0 {
				return s[:i+3] + "xxxxx@" + rest[at+1:]
			}
		}
		return s
	}
	return dsnPasswordRe.ReplaceAllString(s, "${1}xxxxx")
}

// RedactString は s に含まれる secrets（DSN / URL）をそれぞれ RedactURL で伏せた形に置き換え、
// さらに URL から取り出したパスワード単体の出現箇所も "xxxxx" に置き換える。
// DSN が埋め込まれている可能性のあるエラーメッセージに対して使う。
func RedactString(s string, secrets ...string) string {
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		s = strings.ReplaceAll(s, sec, RedactURL(sec))
		if u, err := url.Parse(sec); err == nil {
			if pw, ok := u.User.Password(); ok && pw != "" {
				s = strings.ReplaceAll(s, pw, "xxxxx")
			}
		}
	}
	return s
}

// Retry は外部サービスへの接続処理 fn を最大 attempts 回まで試行する。
// 失敗するたびに sleep だけ待ち、待ち時間を2倍にして再帰的に再試行する（指数バックオフ）。
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
