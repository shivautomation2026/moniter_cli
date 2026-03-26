package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/fsnotify/fsnotify"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	primaryConfigFileName = "monitor_config.json"
)

var discordWebhookURL string

type Config struct {
	PDFFolder         string `json:"pdf_folder"`
	BaseOutputFolder  string `json:"base_output_folder"`
	ClientName        string `json:"client_name"`
	SourceToken       string `json:"source_token"`
	IngestURL         string `json:"ingest_url"`
	S3BucketName      string `json:"s3_bucket_name"`
	S3EndpointURL     string `json:"s3_endpoint_url"`
	S3Region          string `json:"s3_region"`
	S3AccessKey       string `json:"s3_access_key"`
	S3SecretKey       string `json:"s3_secret_key"`
	DiscordWebhookURL string `json:"discord_webhook_url"`
}

type Monitor struct {
	cfg       Config
	logger    *log.Logger
	logFile   *os.File
	remoteLog io.Writer
	s3Client  *s3.Client
	handler   *DynamicFolderHandler
	stopCh    chan struct{}
	stopOnce  sync.Once
}

type App struct {
	ctx         context.Context
	wailsApp    *application.App
	mainWindow  *application.WebviewWindow
	configPath  string
	monitor     *Monitor
	monitorMu   sync.Mutex
	lastStatus  string
	statusMu    sync.Mutex
	allowQuit   bool
	quitMu      sync.Mutex
	showOnStart bool
	showMu      sync.Mutex
}

func buildTray(wailsApp *application.App, app *App) *application.Menu {
	tray := wailsApp.NewMenu()

	tray.Add("Show").OnClick(func(*application.Context) {
		app.ShowWindow()
	})

	tray.Add("Hide").OnClick(func(*application.Context) {
		app.HideWindow()
	})

	tray.AddSeparator()

	tray.Add("Quit").OnClick(func(*application.Context) {
		app.QuitApp()
	})

	return tray
}
func NewApp() *App {
	app := &App{
		configPath: resolveConfigPath(),
		lastStatus: "idle",
	}

	return app
}

func (a *App) setApplication(wailsApp *application.App) {
	a.wailsApp = wailsApp
}

func (a *App) setMainWindow(window *application.WebviewWindow) {
	a.mainWindow = window
}

func (a *App) ShowWindow() {
	if a.mainWindow == nil {
		return
	}
	a.mainWindow.Show()
	a.mainWindow.UnMinimise()
	a.mainWindow.Focus()
}

func (a *App) HideWindow() {
	if a.mainWindow == nil {
		return
	}
	a.mainWindow.Hide()
}

func (a *App) QuitApp() {
	if a.wailsApp == nil {
		return
	}
	a.setAllowQuit(true)
	a.wailsApp.Quit()
}

func (a *App) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	a.ctx = ctx

	cfg, err := a.loadValidatedConfig()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			a.setStatus("config not found")
		} else {
			a.setStatus("config invalid: " + err.Error())
		}
		a.setShowOnStart(true)
		return nil
	}

	if err := a.startMonitor(cfg); err != nil {
		a.setStatus("config found, but failed to start monitor: " + err.Error())
		a.setShowOnStart(true)
	} else {
		a.HideWindow()
		a.setStatus("monitor running")
		a.setShowOnStart(false)
	}

	return nil
}

func (a *App) ServiceShutdown() error {
	return a.StopMonitor()
}

func (a *App) HasConfig() bool {
	_, err := a.loadValidatedConfig()
	return err == nil
}

func (a *App) loadValidatedConfig() (*Config, error) {
	cfg, err := loadConfig(a.configPath)
	if err != nil {
		return nil, err
	}
	if err := validateConfig(*cfg); err != nil {
		return nil, err
	}
	discordWebhookURL = cfg.DiscordWebhookURL

	cfg.PDFFolder = normalizePath(cfg.PDFFolder)
	cfg.BaseOutputFolder = normalizePath(cfg.BaseOutputFolder)

	return cfg, nil
}

func (a *App) GetStatus() string {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	return a.lastStatus
}

func (a *App) setStatus(s string) {
	a.statusMu.Lock()
	a.lastStatus = s
	a.statusMu.Unlock()
}

func (a *App) setAllowQuit(allow bool) {
	a.quitMu.Lock()
	a.allowQuit = allow
	a.quitMu.Unlock()
}

func (a *App) shouldAllowQuit() bool {
	a.quitMu.Lock()
	defer a.quitMu.Unlock()
	return a.allowQuit
}

func (a *App) setShowOnStart(show bool) {
	a.showMu.Lock()
	a.showOnStart = show
	a.showMu.Unlock()
}

func (a *App) shouldShowOnStart() bool {
	a.showMu.Lock()
	defer a.showMu.Unlock()
	return a.showOnStart
}

func (a *App) PickFolder() (string, error) {
	if a.wailsApp == nil {
		return "", fmt.Errorf("application not ready yet")
	}

	return a.wailsApp.Dialog.OpenFile().
		CanChooseDirectories(true).
		CanChooseFiles(false).
		PromptForSingleSelection()
}

func (a *App) SaveConfigAndStart(cfg Config) (string, error) {
	if err := validateConfig(cfg); err != nil {
		return "", err
	}

	cfg.PDFFolder = normalizePath(cfg.PDFFolder)
	cfg.BaseOutputFolder = normalizePath(cfg.BaseOutputFolder)

	if err := os.MkdirAll(cfg.PDFFolder, 0o755); err != nil {
		return "", fmt.Errorf("failed to create pdf folder: %w", err)
	}
	if err := os.MkdirAll(cfg.BaseOutputFolder, 0o755); err != nil {
		return "", fmt.Errorf("failed to create output folder: %w", err)
	}

	if err := saveConfig(a.configPath, &cfg); err != nil {
		return "", err
	}

	if err := a.startMonitor(&cfg); err != nil {
		a.setStatus("failed to start monitor: " + err.Error())
		return "", err
	}

	a.setStatus("monitor running")
	a.HideWindow()

	return "Config saved. Monitor started in tray.", nil
}

func (a *App) StopMonitor() error {
	a.monitorMu.Lock()
	defer a.monitorMu.Unlock()

	if a.monitor == nil {
		a.setStatus("monitor not running")
		return nil
	}
	a.monitor.Stop()
	a.monitor = nil
	a.setStatus("monitor stopped")
	return nil
}

func (a *App) startMonitor(cfg *Config) error {
	a.monitorMu.Lock()
	defer a.monitorMu.Unlock()

	if a.monitor != nil {
		return nil
	}

	monitor, err := NewMonitor(*cfg)
	if err != nil {
		return err
	}
	a.monitor = monitor

	go func() {
		err := monitor.Run()
		if err != nil {
			a.setStatus("monitor stopped with error: " + err.Error())
		} else {
			a.setStatus("monitor stopped")
		}
		a.monitorMu.Lock()
		if a.monitor == monitor {
			a.monitor = nil
		}
		a.monitorMu.Unlock()
	}()

	return nil
}

func validateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.PDFFolder) == "" {
		return fmt.Errorf("pdf_folder is required")
	}
	if strings.TrimSpace(cfg.BaseOutputFolder) == "" {
		return fmt.Errorf("base_output_folder is required")
	}
	if strings.TrimSpace(cfg.S3BucketName) == "" {
		return fmt.Errorf("s3_bucket_name is required")
	}
	if strings.TrimSpace(cfg.S3EndpointURL) == "" {
		return fmt.Errorf("s3_endpoint_url is required")
	}
	if (cfg.SourceToken == "") != (cfg.IngestURL == "") {
		return fmt.Errorf("source_token and ingest_url must be provided together")
	}
	if (cfg.S3EndpointURL != "") && (cfg.S3AccessKey == "" || cfg.S3SecretKey == "") {
		return fmt.Errorf("s3_access_key and s3_secret_key are required when s3_endpoint_url is provided")
	}
	if strings.TrimSpace(cfg.ClientName) == "" {
		return fmt.Errorf("client_name is required")
	}

	return nil
}

func resolveConfigPath() string {
	if envPath := strings.TrimSpace(os.Getenv("MONITER_CONFIG_PATH")); envPath != "" {
		return normalizePath(envPath)
	}

	cwd, _ := os.Getwd()
	exeDir := ""
	if exePath, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exePath)
	}

	return resolveConfigPathForLocations(cwd, exeDir, fileExists)
}

func resolveConfigPathForLocations(cwd, exeDir string, exists func(string) bool) string {
	for _, candidate := range candidateConfigPaths(cwd, exeDir) {
		if exists(candidate) {
			return candidate
		}
	}

	if exeDir != "" {
		return filepath.Join(exeDir, primaryConfigFileName)
	}
	if cwd != "" {
		return filepath.Join(cwd, primaryConfigFileName)
	}
	return primaryConfigFileName
}

func candidateConfigPaths(cwd, exeDir string) []string {
	dirs := uniqueNonEmptyPaths(
		exeDir,
		cwd,
	)

	candidates := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		candidates = append(candidates, filepath.Join(dir, primaryConfigFileName))
	}

	return candidates
}

func uniqueNonEmptyPaths(paths ...string) []string {
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))

	for _, path := range paths {
		if path == "" {
			continue
		}
		cleaned := filepath.Clean(path)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		result = append(result, cleaned)
	}

	return result
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func saveConfig(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func normalizePath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	abs, err := filepath.Abs(p)
	if err == nil {
		return abs
	}
	return p
}

func NewMonitor(cfg Config) (*Monitor, error) {
	logger, logFile, remoteLog, err := configureLogging(cfg)
	if err != nil {
		return nil, err
	}

	s3Client, err := getS3Client(cfg, logger)
	if err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return nil, err
	}

	return &Monitor{
		cfg:       cfg,
		logger:    logger,
		logFile:   logFile,
		remoteLog: remoteLog,
		s3Client:  s3Client,
		stopCh:    make(chan struct{}),
	}, nil
}

func (m *Monitor) Run() error {
	if err := verifyS3Credentials(context.Background(), m.s3Client, m.cfg.S3BucketName, m.logger, m.cfg.ClientName); err != nil {
		return err
	}

	handler, err := NewDynamicFolderHandler(m.cfg.PDFFolder, m.s3Client, m.cfg.S3BucketName, m.logger, m.cfg.ClientName)
	if err != nil {
		return err
	}
	m.handler = handler

	if err := handler.StartMonitoring(); err != nil {
		m.logger.Printf("%s Waiting for today's folder: %s", m.cfg.ClientName, handler.currentFolder)
	}

	m.logger.Printf("Monitoring Started for client: %s", m.cfg.ClientName)
	m.logger.Printf("%s Base folder: %s", m.cfg.ClientName, m.cfg.PDFFolder)
	m.logger.Printf("%s Current monitoring folder: %s", m.cfg.ClientName, handler.currentFolder)
	m.logger.Printf("%s Logs folder: %s", m.cfg.ClientName, m.cfg.BaseOutputFolder)
	m.logger.Printf("%s The program will automatically switch to new day's folder when available.", m.cfg.ClientName)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			handler.Stop()
			if m.logFile != nil {
				_ = m.logFile.Close()
			}
			m.logger.Printf("%s Monitoring stopped", m.cfg.ClientName)
			return nil
		case <-ticker.C:
			if err := handler.CheckForNewDay(); err != nil {
				m.logger.Printf("%s check_for_new_day error: %v", m.cfg.ClientName, err)
			}
		}
	}
}

func (m *Monitor) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
}

func configureLogging(cfg Config) (*log.Logger, *os.File, io.Writer, error) {
	if err := os.MkdirAll(cfg.BaseOutputFolder, 0o755); err != nil {
		return nil, nil, nil, err
	}

	logFilePath := filepath.Join(cfg.BaseOutputFolder, "log_"+time.Now().Format("2006-01-02")+".txt")
	f, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, nil, err
	}

	writers := []io.Writer{f}
	var remote io.Writer
	if cfg.SourceToken != "" && cfg.IngestURL != "" {
		remote = &RemoteLogWriter{
			sourceToken: cfg.SourceToken,
			ingestURL:   cfg.IngestURL,
			client:      &http.Client{Timeout: 5 * time.Second},
		}
		writers = append(writers, remote)
	}
	writers = append(writers, os.Stdout)

	logger := log.New(newResilientMultiWriter(writers...), "", log.LstdFlags)
	logger.Printf("Daily Log File: %s", logFilePath)
	logger.Printf("Configuration loaded for client: %s", cfg.ClientName)
	return logger, f, remote, nil
}

type RemoteLogWriter struct {
	sourceToken string
	ingestURL   string
	client      *http.Client
}

type resilientMultiWriter struct {
	writers []io.Writer
}

func newResilientMultiWriter(writers ...io.Writer) io.Writer {
	filtered := make([]io.Writer, 0, len(writers))
	for _, writer := range writers {
		if writer != nil {
			filtered = append(filtered, writer)
		}
	}
	return &resilientMultiWriter{writers: filtered}
}

func (w *resilientMultiWriter) Write(p []byte) (int, error) {
	var firstErr error
	shortWrite := false
	wroteAny := false

	for _, writer := range w.writers {
		n, err := writer.Write(p)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if n != len(p) {
			shortWrite = true
			continue
		}
		wroteAny = true
	}

	if wroteAny {
		return len(p), nil
	}
	if shortWrite {
		return 0, io.ErrShortWrite
	}
	if firstErr == nil {
		firstErr = io.ErrClosedPipe
	}
	return 0, firstErr
}

func (w *RemoteLogWriter) Write(p []byte) (int, error) {
	payload := map[string]any{
		"message":   strings.TrimSpace(string(p)),
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	b, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, w.ingestURL, strings.NewReader(string(b)))
	if err != nil {
		return len(p), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.sourceToken)

	resp, err := w.client.Do(req)
	if err != nil {
		return len(p), nil
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return len(p), nil
}

func getS3Client(cfg Config, logger *log.Logger) (*s3.Client, error) {
	ctx := context.Background()

	if strings.TrimSpace(cfg.S3EndpointURL) == "" {
		logger.Printf("[%s] Creating S3 client with default AWS configuration.", cfg.ClientName)
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.S3Region))
		if err != nil {
			return nil, err
		}
		return s3.NewFromConfig(awsCfg), nil
	}

	logger.Printf("[%s] Creating S3 client with custom endpoint. endpoint_url=%s region=%s", cfg.ClientName, cfg.S3EndpointURL, cfg.S3Region)

	awsCfg, err := awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		),
	)
	if err != nil {
		return nil, err
	}

	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.EndpointResolver = s3.EndpointResolverFromURL(cfg.S3EndpointURL)
		o.UsePathStyle = true
	}), nil
}

func verifyS3Credentials(ctx context.Context, s3Client *s3.Client, bucketName string, logger *log.Logger, clientName string) error {
	if strings.TrimSpace(bucketName) == "" {
		return fmt.Errorf("s3 bucket name is missing from configuration")
	}
	logger.Printf("[%s] Verifying S3 credentials for bucket: %s", clientName, bucketName)
	_, err := s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		logger.Printf("[%s] Unable to access S3 bucket %s. Error: %v", clientName, bucketName, err)
		return err
	}
	logger.Printf("[%s] S3 credentials verified for bucket: %s", clientName, bucketName)
	return nil
}

func uploadFileToObjectStore(ctx context.Context, s3Client *s3.Client, filePath, bucketName, s3Key string, logger *log.Logger, clientName string) error {
	s3Key = fmt.Sprintf("%s_%d_%s", clientName, time.Now().Unix(), s3Key)

	logger.Printf("[%s] Uploading file to S3: bucket=%s key=%s path=%s", clientName, bucketName, s3Key, filePath)

	f, err := os.Open(filePath)
	if err != nil {
		logger.Printf("[%s] Upload failed: %v", clientName, err)
		return err
	}
	defer f.Close()

	_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucketName),
		Key:         aws.String(s3Key),
		ContentType: aws.String("application/pdf"),
		Body:        f,
	})
	if err != nil {
		sendDiscordNotification(discordWebhookURL, fmt.Sprintf("Failed to upload file to S3: %v", err), logger, clientName)
		logger.Printf("[%s] Upload failed: %v", clientName, err)
		return err
	}
	sendDiscordNotification(discordWebhookURL, fmt.Sprintf("[%s] File uploaded to S3 successfully: bucket=%s filename=%s", clientName, bucketName, s3Key), logger, clientName)
	logger.Printf("[%s] Upload completed: bucket=%s key=%s", clientName, bucketName, s3Key)
	return nil
}

func processPDF(ctx context.Context, s3Client *s3.Client, bucketName, pdfPath string, logger *log.Logger, clientName string) {
	logger.Printf("[%s] Processing new PDF: %s", clientName, pdfPath)
	if err := uploadFileToObjectStore(ctx, s3Client, pdfPath, bucketName, filepath.Base(pdfPath), logger, clientName); err != nil {
		logger.Printf("[%s] process_pdf failed: %v", clientName, err)
	}
}

func getCurrentDayFolder(basePath string) string {
	return filepath.Join(basePath, time.Now().Format("2006-01-02"))
}

func ensureDir(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	if err := os.MkdirAll(path, 0o755); err != nil {
		return false, err
	}

	return true, nil
}

type DynamicFolderHandler struct {
	baseFolderPath string
	currentFolder  string
	processedFiles map[string]struct{}
	processing     map[string]struct{}
	watcher        *fsnotify.Watcher
	lastCheckTime  time.Time
	checkInterval  time.Duration
	s3Client       *s3.Client
	bucketName     string
	logger         *log.Logger
	mu             sync.Mutex
	clientName     string
}

func NewDynamicFolderHandler(baseFolderPath string, s3Client *s3.Client, bucketName string, logger *log.Logger, clientName string) (*DynamicFolderHandler, error) {
	return &DynamicFolderHandler{
		baseFolderPath: baseFolderPath,
		currentFolder:  getCurrentDayFolder(baseFolderPath),
		processedFiles: make(map[string]struct{}),
		processing:     make(map[string]struct{}),
		lastCheckTime:  time.Now(),
		checkInterval:  60 * time.Second,
		s3Client:       s3Client,
		bucketName:     bucketName,
		logger:         logger,
		clientName:     clientName,
	}, nil
}

func (h *DynamicFolderHandler) StartMonitoring() error {
	created, err := ensureDir(h.currentFolder)
	if err != nil {
		return err
	}
	if created {
		h.logger.Printf("[%s] Created current day folder: %s", h.clientName, h.currentFolder)
	}

	if h.watcher != nil {
		h.Stop()
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := w.Add(h.currentFolder); err != nil {
		w.Close()
		return err
	}

	h.watcher = w
	go h.watchLoop()
	h.logger.Printf("[%s] Started monitoring %s", h.clientName, h.currentFolder)
	return nil
}

func (h *DynamicFolderHandler) Stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.watcher != nil {
		h.logger.Printf("[%s] Stopping current observer.", h.clientName)
		_ = h.watcher.Close()
		h.watcher = nil
		h.logger.Printf("[%s] Observer stopped.", h.clientName)
	}
}

func (h *DynamicFolderHandler) CheckForNewDay() error {
	h.mu.Lock()
	interval := h.checkInterval
	if h.watcher == nil {
		interval = 5 * time.Second
	}
	if time.Since(h.lastCheckTime) < interval {
		h.mu.Unlock()
		return nil
	}
	h.lastCheckTime = time.Now()
	expectedFolder := getCurrentDayFolder(h.baseFolderPath)
	currentFolder := h.currentFolder
	watcherNil := h.watcher == nil
	h.mu.Unlock()

	created, err := ensureDir(expectedFolder)
	if err != nil {
		return err
	}
	if created {
		h.logger.Printf("[%s] Created current day folder: %s", h.clientName, expectedFolder)
	}

	if watcherNil {
		h.mu.Lock()
		h.currentFolder = expectedFolder
		h.mu.Unlock()
		h.logger.Printf("[%s] Observer not running; starting monitoring for %s", h.clientName, expectedFolder)
		return h.StartMonitoring()
	}

	if expectedFolder != currentFolder {
		h.logger.Printf("[%s] New day folder detected: %s", h.clientName, expectedFolder)
		h.Stop()
		h.mu.Lock()
		h.currentFolder = expectedFolder
		h.processedFiles = make(map[string]struct{})
		h.processing = make(map[string]struct{})
		h.mu.Unlock()
		return h.StartMonitoring()
	}

	return nil
}

func (h *DynamicFolderHandler) watchLoop() {
	for {
		h.mu.Lock()
		w := h.watcher
		h.mu.Unlock()

		if w == nil {
			return
		}

		select {
		case event, ok := <-w.Events:
			if !ok {
				return
			}

			if event.Has(fsnotify.Create) || event.Has(fsnotify.Write) || event.Has(fsnotify.Rename) {
				info, err := os.Stat(event.Name)
				if err == nil && info.IsDir() {
					h.logger.Printf("[%s] Directory event ignored: %s", h.clientName, event.Name)
					continue
				}
				h.processFile(event.Name, h.clientName)
			}

		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			h.logger.Printf("[%s] Watcher error: %v", h.clientName, err)
		}
	}
}

func (h *DynamicFolderHandler) processFile(filePath string, clientName string) {
	if !strings.HasSuffix(strings.ToLower(filePath), ".pdf") {
		h.logger.Printf("[%s] Skipping non-PDF file: %s", clientName, filePath)
		return
	}

	h.mu.Lock()
	if _, ok := h.processedFiles[filePath]; ok {
		h.mu.Unlock()
		h.logger.Printf("[%s] Skipping already processed file: %s", clientName, filePath)
		return
	}
	if _, ok := h.processing[filePath]; ok {
		h.mu.Unlock()
		return
	}
	h.processing[filePath] = struct{}{}
	h.mu.Unlock()

	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.processing, filePath)
			h.mu.Unlock()
		}()

		if !h.isFileReady(filePath, 30*time.Second, 1*time.Second) {
			h.logger.Printf("[%s] File not ready or timed out: %s", h.clientName, filePath)
			return
		}

		h.mu.Lock()
		if _, ok := h.processedFiles[filePath]; ok {
			h.mu.Unlock()
			h.logger.Printf("[%s] Skipping already processed file: %s", h.clientName, filePath)
			return
		}
		h.processedFiles[filePath] = struct{}{}
		h.mu.Unlock()

		h.logger.Printf("[%s] New PDF detected (processed): %s", h.clientName, filePath)
		processPDF(context.Background(), h.s3Client, h.bucketName, filePath, h.logger, h.clientName)
	}()
}

func (h *DynamicFolderHandler) isFileReady(filePath string, timeout, checkInterval time.Duration) bool {
	start := time.Now()
	var lastSize int64 = -1

	for time.Since(start) < timeout {
		info, err := os.Stat(filePath)
		if err != nil {
			h.logger.Printf("[%s] File not found while checking readiness: %s", h.clientName, filePath)
			return false
		}
		size := info.Size()
		if size == lastSize {
			h.logger.Printf("[%s] File ready: %s", h.clientName, filePath)
			return true
		}
		lastSize = size
		time.Sleep(checkInterval)
	}
	h.logger.Printf("[%s] Timeout waiting for %s to be ready", h.clientName, filePath)
	return false
}
