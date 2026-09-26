package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ... (他の構造体定義は変更なし)
type ContainerStats struct {
	CPUPercent float64 `json:"cpuPercent"`
	MemUsage   float64 `json:"memUsage"`
	MemLimit   float64 `json:"memLimit"`
}

type LogMessage struct {
	ContainerID   string    `json:"containerId"`
	ContainerName string    `json:"containerName"`
	Timestamp     time.Time `json:"timestamp"`
	Message       string    `json:"message"`
	IsError       bool      `json:"isError"`
}

type ContainerInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Image        string   `json:"image"`
	Status       string   `json:"status"`
	State        string   `json:"state"`
	Health       string   `json:"health"`
	Created      time.Time `json:"created"`
	StartedAt    string    `json:"startedAt"`
	Uptime       string    `json:"uptime"`
	RestartCount int       `json:"restartCount"`
	Networks     []string  `json:"networks"`
	IsError      bool      `json:"isError"`
}

type TopologyNode struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Networks []string `json:"networks"`
}

type VolumeInfo struct {
	Name       string              `json:"name"`
	Driver     string              `json:"driver"`
	Mountpoint string              `json:"mountpoint"`
	Labels     map[string]string   `json:"labels"`
}

type MinioItem struct {
	Name         string    `json:"name"`
	Type         string    `json:"type"` // "bucket", "folder", "file"
	Size         int64     `json:"size,omitempty"`
	LastModified time.Time `json:"lastModified,omitempty"`
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

var errorKeywords = []string{"ERROR", "FATAL", "panic", "Exception", "failed", "denied", "level=error"}

func main() {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}

	router := mux.NewRouter()

	// CORSミドルウェア
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next.ServeHTTP(w, r)
		})
	})

	router.HandleFunc("/api/containers", getContainers(cli)).Methods("GET")
	router.HandleFunc("/api/containers/stats", getContainerStats(cli)).Methods("GET")
	router.HandleFunc("/api/containers/restart/{name}", restartContainer(cli)).Methods("POST", "OPTIONS")
	router.HandleFunc("/api/connections/count", getActiveUserCount(cli)).Methods("GET")
	router.HandleFunc("/api/topology", getTopology(cli)).Methods("GET")
	router.HandleFunc("/api/volumes", listVolumes(cli)).Methods("GET")

	// ルーティング修正: パス変数とクエリパラメーターの両方に対応
	router.HandleFunc("/ws/logs", serveWs(cli)).Methods("GET")
	router.HandleFunc("/ws/logs/{id}", serveWs(cli)).Methods("GET")

	// MinIO API
	router.HandleFunc("/api/minio/list/{containerName}", listMinioObjects(cli)).Methods("GET", "OPTIONS")
	router.HandleFunc("/api/minio/upload/{containerName}", uploadMinioObject(cli)).Methods("POST", "OPTIONS")
	router.HandleFunc("/api/minio/delete/{containerName}", deleteMinioObject(cli)).Methods("DELETE", "OPTIONS")

	log.Println("Monitoring backend started on :8080")
	log.Fatal(http.ListenAndServe(":8080", router))
}

func getContainerStats(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		containers, err := cli.ContainerList(ctx, container.ListOptions{})
		if err != nil {
			http.Error(w, "Failed to list containers", http.StatusInternalServerError)
			return
		}

		statsMap := make(map[string]ContainerStats)
		var wg sync.WaitGroup
		var mu sync.Mutex

		for _, c := range containers {
			if c.State != "running" {
				continue
			}
			wg.Add(1)
			go func(c types.Container) {
				defer wg.Done()
				stats, err := cli.ContainerStats(ctx, c.ID, false)
				if err != nil {
					log.Printf("Failed to get stats for container %s: %v", c.ID, err)
					return
				}
				defer stats.Body.Close()

				var v types.StatsJSON
				if err := json.NewDecoder(stats.Body).Decode(&v); err != nil {
					log.Printf("Failed to decode stats for container %s: %v", c.ID, err)
					return
				}

				cpuPercent := calculateCPUPercent(&v)
				memUsage := float64(v.MemoryStats.Usage)
				memLimit := float64(v.MemoryStats.Limit)

				mu.Lock()
				name := strings.TrimPrefix(c.Names[0], "/")
				statsMap[name] = ContainerStats{
					CPUPercent: cpuPercent,
					MemUsage:   memUsage,
					MemLimit:   memLimit,
				}
				mu.Unlock()
			}(c)
		}

		wg.Wait()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(statsMap)
	}
}

func calculateCPUPercent(v *types.StatsJSON) float64 {
	cpuDelta := float64(v.CPUStats.CPUUsage.TotalUsage) - float64(v.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(v.CPUStats.SystemUsage) - float64(v.PreCPUStats.SystemUsage)

	if systemDelta > 0.0 && cpuDelta > 0.0 {
		return (cpuDelta / systemDelta) * float64(len(v.CPUStats.CPUUsage.PercpuUsage)) * 100.0
	}
	return 0.0
}

func getActiveUserCount(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		options := container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Since:      "5m", // 直近5分間のログを取得
		}

		// "atmosidea-frontend" という名前のコンテナを探す
		containers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
		if err != nil {
			http.Error(w, "Failed to list containers", http.StatusInternalServerError)
			return
		}

		var frontendContainerID string
		for _, c := range containers {
			for _, name := range c.Names {
				if strings.Contains(name, "atmosidea-frontend") {
					frontendContainerID = c.ID
					break
				}
			}
			if frontendContainerID != "" {
				break
			}
		}

		if frontendContainerID == "" {
			http.Error(w, "atmosidea-frontend container not found", http.StatusNotFound)
			return
		}

		reader, err := cli.ContainerLogs(ctx, frontendContainerID, options)
		if err != nil {
			http.Error(w, "Failed to get logs for frontend container", http.StatusInternalServerError)
			return
		}
		defer reader.Close()

		ipRegex := regexp.MustCompile(`(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})`)
		uniqueIPs := make(map[string]bool)

		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			line := scanner.Text()
			// Dockerログのヘッダー（8バイト）をスキップ
			if len(line) > 8 {
				line = line[8:]
			}
			matches := ipRegex.FindStringSubmatch(line)
			if len(matches) > 1 {
				uniqueIPs[matches[1]] = true
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"count": len(uniqueIPs)})
	}
}

func getContainers(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		containers, err := cli.ContainerList(context.Background(), container.ListOptions{All: true})
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to list containers: %v", err), http.StatusInternalServerError)
			return
		}

		var containerInfos []ContainerInfo
		var mu sync.Mutex
		var wg sync.WaitGroup

		for _, c := range containers {
			wg.Add(1)
			go func(c types.Container) {
				defer wg.Done()
				name := strings.TrimPrefix(c.Names[0], "/")
				createdTime := time.Unix(c.Created, 0)

				meta := inspectContainerMeta(cli, c.ID)
				info := ContainerInfo{
					ID:           c.ID[:12],
					Name:         name,
					Image:        c.Image,
					Status:       c.Status,
					State:        c.State,
					RestartCount: meta.restartCount,
					Created:      createdTime,
					Networks:     getContainerNetworks(cli, c.ID),
					Health:       meta.health,
					StartedAt:    meta.startedAt,
					Uptime:       getContainerUptime(meta.startedAt),
					IsError:      false,
				}
				mu.Lock()
				containerInfos = append(containerInfos, info)
				mu.Unlock()
			}(c)
		}
		wg.Wait()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(containerInfos)
	}
}

func getContainerNetworks(cli *client.Client, id string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, err := cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil
	}
	networks := make([]string, 0, len(info.NetworkSettings.Networks))
	for name := range info.NetworkSettings.Networks {
		networks = append(networks, name)
	}
	return networks
}

type containerMeta struct {
	restartCount int
	health       string
	startedAt    string
}

func inspectContainerMeta(cli *client.Client, id string) containerMeta {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	info, err := cli.ContainerInspect(ctx, id)
	if err != nil || info.State == nil {
		return containerMeta{}
	}
	var health string
	if info.State.Health != nil {
		health = info.State.Health.Status
	}
	var startedAt string
	if info.State.StartedAt != "" {
		startedAt = info.State.StartedAt
	}
	return containerMeta{
		restartCount: int(info.RestartCount),
		health:       health,
		startedAt:    startedAt,
	}
}

func getContainerUptime(startedAt string) string {
	if startedAt == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339Nano, startedAt); err == nil {
		return time.Since(t).Round(time.Second).String()
	}
	return ""
}

func getTopology(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		containers, err := cli.ContainerList(context.Background(), container.ListOptions{All: true})
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to list containers: %v", err), http.StatusInternalServerError)
			return
		}

		nodes := make([]TopologyNode, 0, len(containers))
		for _, c := range containers {
			name := strings.TrimPrefix(c.Names[0], "/")
			nodes = append(nodes, TopologyNode{
				ID:       c.ID[:12],
				Name:     name,
				Networks: getContainerNetworks(cli, c.ID),
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(nodes)
	}
}

func listVolumes(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		resp, err := cli.VolumeList(ctx, volume.ListOptions{})
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to list volumes: %v", err), http.StatusInternalServerError)
			return
		}

		vols := make([]VolumeInfo, 0, len(resp.Volumes))
		for _, v := range resp.Volumes {
			vols = append(vols, VolumeInfo{
				Name:       v.Name,
				Driver:     v.Driver,
				Mountpoint: v.Mountpoint,
				Labels:     v.Labels,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(vols)
	}
}

// コンテナ再起動処理
func restartContainer(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		containerName := vars["name"]

		timeout := 10
		stopOptions := container.StopOptions{Timeout: &timeout}

		err := cli.ContainerRestart(context.Background(), containerName, stopOptions)
		if err != nil {
			log.Printf("[Error] Failed to restart container %s: %v", containerName, err)
			http.Error(w, fmt.Sprintf("Failed to restart container: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"message": "Container restarted successfully"})
	}
}

func serveWs(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		containerTarget := vars["id"]
		if containerTarget == "" {
			containerTarget = r.URL.Query().Get("container")
		}

		if containerTarget == "" {
			http.Error(w, "Container ID or Name is required", http.StatusBadRequest)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("Failed to upgrade to websocket: %v", err)
			return
		}
		defer conn.Close()

		containerJSON, err := cli.ContainerInspect(context.Background(), containerTarget)
		if err != nil {
			log.Printf("Failed to inspect container %s: %v", containerTarget, err)
			conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Error inspecting container: %v", err)))
			return
		}
		containerID := containerJSON.ID[:12]
		containerName := strings.TrimPrefix(containerJSON.Name, "/")

		options := container.LogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
			Timestamps: true,
			Tail:       "100",
		}

		reader, err := cli.ContainerLogs(context.Background(), containerTarget, options)
		if err != nil {
			log.Printf("Failed to get logs for container %s: %v", containerTarget, err)
			return
		}
		defer reader.Close()

		scanner := NewDockerLogScanner(reader)
		for scanner.Scan() {
			line := scanner.Text()
			timestampStr := ""
			messageContent := line

			if len(line) >= 30 && line[23] == 'Z' {
				timestampStr = line[:30]
				messageContent = line[31:]
			}

			parsedTime, err := time.Parse(time.RFC3339Nano, timestampStr)
			if err != nil {
				parsedTime = time.Now()
			}

			isError := false
			for _, keyword := range errorKeywords {
				if strings.Contains(strings.ToLower(messageContent), strings.ToLower(keyword)) {
					isError = true
					break
				}
			}

			msg := LogMessage{
				ContainerID:   containerID,
				ContainerName: containerName,
				Timestamp:     parsedTime,
				Message:       messageContent,
				IsError:       isError,
			}

			if err := conn.WriteMessage(websocket.TextMessage, []byte(msg.Message)); err != nil {
				break
			}
		}
	}
}

func getMinioClient(cli *client.Client, containerName string) (*minio.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	containerJSON, err := cli.ContainerInspect(ctx, containerName)
	if err != nil {
		return nil, fmt.Errorf("container inspect failed: %w", err)
	}

	envMap := make(map[string]string)
	for _, env := range containerJSON.Config.Env {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	accessKeyID := envMap["MINIO_ROOT_USER"]
	if accessKeyID == "" {
		accessKeyID = envMap["MINIO_ACCESS_KEY"]
	}
	secretAccessKey := envMap["MINIO_ROOT_PASSWORD"]
	if secretAccessKey == "" {
		secretAccessKey = envMap["MINIO_SECRET_KEY"]
	}

	endpoint := containerName + ":9000"

	return minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: false,
	})
}

func listMinioObjects(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		vars := mux.Vars(r)
		containerName := vars["containerName"]
		bucketName := r.URL.Query().Get("bucket")
		prefix := r.URL.Query().Get("prefix")

		minioClient, err := getMinioClient(cli, containerName)
		if err != nil {
			json.NewEncoder(w).Encode([]MinioItem{})
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		var minioItems []MinioItem

		if bucketName == "" {
			buckets, err := minioClient.ListBuckets(ctx)
			if err != nil {
				json.NewEncoder(w).Encode([]MinioItem{})
				return
			}
			for _, bucket := range buckets {
				minioItems = append(minioItems, MinioItem{
					Name: bucket.Name,
					Type: "bucket",
				})
			}
		} else {
			objectCh := minioClient.ListObjects(ctx, bucketName, minio.ListObjectsOptions{
				Prefix:    prefix,
				Recursive: false,
			})

			folders := make(map[string]struct{})

			for object := range objectCh {
				if object.Err != nil {
					continue
				}

				if object.Key == prefix {
					continue
				}

				relKey := strings.TrimPrefix(object.Key, prefix)
				if relKey == "" {
					continue
				}

				if strings.HasSuffix(object.Key, "/") || strings.Contains(relKey, "/") {
					parts := strings.Split(relKey, "/")
					folderName := parts[0]
					if folderName != "" {
						if _, exists := folders[folderName]; !exists {
							minioItems = append(minioItems, MinioItem{
								Name: folderName,
								Type: "folder",
							})
							folders[folderName] = struct{}{}
						}
					}
				} else {
					minioItems = append(minioItems, MinioItem{
						Name:         relKey,
						Type:         "file",
						Size:         object.Size,
						LastModified: object.LastModified,
					})
				}
			}
		}

		json.NewEncoder(w).Encode(minioItems)
	}
}

func uploadMinioObject(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		containerName := vars["containerName"]
		bucketName := r.URL.Query().Get("bucket")
		prefix := r.URL.Query().Get("prefix")

		minioClient, err := getMinioClient(cli, containerName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		err = r.ParseMultipartForm(32 << 20)
		if err != nil {
			http.Error(w, "File too large", http.StatusBadRequest)
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "Missing file param", http.StatusBadRequest)
			return
		}
		defer file.Close()

		objectKey := prefix + header.Filename
		_, err = minioClient.PutObject(context.Background(), bucketName, objectKey, file, header.Size, minio.PutObjectOptions{
			ContentType: header.Header.Get("Content-Type"),
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to upload: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "Uploaded successfully"})
	}
}

func deleteMinioObject(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		containerName := vars["containerName"]
		bucketName := r.URL.Query().Get("bucket")
		objectKey := r.URL.Query().Get("key")

		minioClient, err := getMinioClient(cli, containerName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		err = minioClient.RemoveObject(context.Background(), bucketName, objectKey, minio.RemoveObjectOptions{})
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to delete: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "Deleted successfully"})
	}
}

type DockerLogScanner struct {
	reader *bufio.Reader
	text   string
	err    error
}

func NewDockerLogScanner(reader io.Reader) *DockerLogScanner {
	return &DockerLogScanner{reader: bufio.NewReader(reader)}
}

func (s *DockerLogScanner) Scan() bool {
	header := make([]byte, 8)
	_, err := io.ReadFull(s.reader, header)
	if err != nil {
		s.err = err
		return false
	}

	payloadSize := binary.BigEndian.Uint32(header[4:8])
	payload := make([]byte, payloadSize)
	_, err = io.ReadFull(s.reader, payload)
	if err != nil {
		s.err = err
		return false
	}

	s.text = strings.TrimSuffix(string(payload), "\n")
	return true
}

func (s *DockerLogScanner) Text() string { return s.text }
func (s *DockerLogScanner) Err() error   { return s.err }