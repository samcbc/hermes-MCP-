package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	mcpURL        = flag.String("mcp-url", "http://192.168.0.10:8082", "MCP server URL to connect to")
	mcpPassword   = flag.String("mcp-password", "vmware123", "MCP server password")
	port          = flag.Int("port", 8080, "Port to listen on")
	password      = flag.String("password", "vmware123", "Auth password")
	installFlag   = flag.Bool("install", false, "Install autostart")
	uninstallFlag = flag.Bool("uninstall", false, "Remove autostart")
	serviceFlag   = flag.Bool("service", false, "Run as Windows service")
	autoStartFlag = flag.Bool("auto", false, "Auto-connect to MCP server")
)

// ===== 命令执行 =====

type ExecRequest struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

type ExecResponse struct {
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
	ExitCode int    `json:"exit_code"`
}

// ===== 文件收发 =====

// FileSendRequest 发送文件请求
type FileSendRequest struct {
	Filename  string   `json:"filename"`       // VM 端保存路径
	Chunks    []string `json:"chunks"`         // base64 编码的字符串块
	TotalSize int64    `json:"total_size"`     // 总大小
	IsFinal   bool     `json:"is_final"`       // 是否最后一个块
}

// FileSendResponse 发送文件响应
type FileSendResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// FileReceiveRequest 接收文件请求
type FileReceiveRequest struct {
	Filename string `json:"filename"` // VM 端文件路径
}

// FileReceiveResponse 接收文件响应
type FileReceiveResponse struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Chunks   []struct {
		Index int    `json:"index"`
		Chunk string `json:"chunk"` // base64 编码的字节块
	} `json:"chunks"`
	Error string `json:"error,omitempty"`
}

// ===== MCP 连接 =====

var (
	mcpClient       *MCPClient
	mcpClientMu     sync.Mutex
	isConnected     = false
)

type MCPClient struct {
	mu        sync.Mutex
	commands  chan MCPCommand
	responses chan MCPResponse
	serverURL string
	password  string
}

type MCPCommand struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type MCPResponse struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Output  string `json:"output"`
	Error   string `json:"error,omitempty"`
	ExitCode int   `json:"exit_code"`
}

func NewMCPClient(serverURL, password string) *MCPClient {
	return &MCPClient{
		commands:  make(chan MCPCommand, 100),
		responses: make(chan MCPResponse, 100),
		serverURL: serverURL,
		password:  password,
	}
}

func (c *MCPClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	resp, err := http.Get(c.serverURL + "/health")
	if err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed: status %d", resp.StatusCode)
	}

	return nil
}

// ===== 主函数 =====

func main() {
	flag.Parse()

	exePath, err := os.Executable()
	if err != nil {
		exePath = "unknown"
	}

	if *installFlag {
		installService(exePath)
		fmt.Printf("Service installed: %s\n", exePath)
		return
	}

	if *uninstallFlag {
		uninstallService()
		fmt.Println("Service uninstalled")
		return
	}

	if *serviceFlag {
		runService(exePath)
		return
	}

	// Normal mode
	mcpClient = NewMCPClient(*mcpURL, *mcpPassword)

	http.HandleFunc("/exec", handleExec)
	http.HandleFunc("/file/send", handleFileSend)
	http.HandleFunc("/file/receive", handleFileReceive)
	http.HandleFunc("/status", handleStatus)
	http.HandleFunc("/health", handleHealth)

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("MCP-Go-Server started on %s (password: %s, pid: %d, exe: %s, mcp-url: %s)\n", addr, *password, os.Getpid(), exePath, *mcpURL)
	fmt.Println("Features: exec, file/send, file/receive")
	fmt.Println("Press Ctrl+C to stop")

	// Connect to MCP server
	fmt.Println("Connecting to MCP server...")
	if err := mcpClient.Connect(); err == nil {
		isConnected = true
		fmt.Println("MCP server connected successfully!")
	} else {
		fmt.Printf("MCP server connection failed: %v (will retry)\n", err)
		go retryMCPConnection()
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		fmt.Println("\nShutting down...")
		os.Exit(0)
	}()

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Listen: %v", err)
	}
}

func retryMCPConnection() {
	for {
		if !isConnected {
			if err := mcpClient.Connect(); err == nil {
				isConnected = true
				fmt.Println("MCP server reconnected!")
			} else {
				fmt.Printf("MCP server reconnect failed: %v (retrying in 10s)\n", err)
				time.Sleep(10 * time.Second)
			}
		} else {
			time.Sleep(30 * time.Second)
		}
	}
}

// ===== 安装/卸载服务 =====

func installService(exePath string) {
	serviceName := "MCPGoServer"
	scriptPath := filepath.Join(filepath.Dir(exePath), "MCP-Go-Server.bat")
	scriptContent := fmt.Sprintf(`@echo off
cd /d "%s"
"%s" --port %d --password "%s" --mcp-url "%s" --mcp-password "%s" --service --auto
`, filepath.Dir(exePath), exePath, *port, *password, *mcpURL, *mcpPassword)
	os.WriteFile(scriptPath, []byte(scriptContent), 0755)

	cmd := exec.Command("sc", "create", serviceName, "binPath=", fmt.Sprintf(`"%s"`, scriptPath), "start=auto", "displayName=MCP Go Server")
	output, err := cmd.CombinedOutput()
	if err == nil {
		log.Printf("sc create output: %s", string(output))
	}

	registryCmd := exec.Command("reg", "add", `HKLM\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "MCPGoServer", "/t", "REG_SZ", "/d", fmt.Sprintf(`"%s" --port %d --password "%s" --mcp-url "%s" --mcp-password "%s" --service --auto`, exePath, *port, *password, *mcpURL, *mcpPassword), "/f")
	registryCmd.Run()

	fmt.Printf("Service '%s' installed\n", serviceName)
}

func uninstallService() {
	exec.Command("sc", "delete", "MCPGoServer").Run()
	exec.Command("reg", "delete", `HKLM\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "MCPGoServer", "/f").Run()
}

func runService(exePath string) {
	logFile := filepath.Join(os.Getenv("TEMP"), "mcp-server.log")
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		log.SetOutput(f)
		defer f.Close()
	}

	log.Printf("Service started, pid: %d", os.Getpid())

	mcpClient = NewMCPClient(*mcpURL, *mcpPassword)

	http.HandleFunc("/exec", handleExec)
	http.HandleFunc("/file/send", handleFileSend)
	http.HandleFunc("/file/receive", handleFileReceive)
	http.HandleFunc("/status", handleStatus)
	http.HandleFunc("/health", handleHealth)

	// Connect to MCP server
	log.Printf("Connecting to MCP server: %s", *mcpURL)
	if err := mcpClient.Connect(); err == nil {
		isConnected = true
		log.Printf("MCP server connected!")
	} else {
		log.Printf("MCP server connection failed: %v (will retry)", err)
		go retryMCPConnection()
	}

	addr := fmt.Sprintf(":%d", *port)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Listen: %v", err)
	}
}

// ===== 健康检查 =====

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "ok",
		"mcp_connected": isConnected,
		"features":      []string{"exec", "file/send", "file/receive"},
	})
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "running",
		"mcp_connected": isConnected,
		"mcp_url":       *mcpURL,
		"port":          *port,
		"features":      []string{"exec", "file/send", "file/receive"},
	})
}

// ===== 命令执行 =====

func handleExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(ExecResponse{Error: "Method not allowed", ExitCode: 1})
		return
	}

	// Auth
	auth := r.Header.Get("Authorization")
	if auth != *password && auth != "Bearer "+*password {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(ExecResponse{Error: "Unauthorized", ExitCode: 1})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ExecResponse{Error: "Read body failed", ExitCode: 1})
		return
	}

	var req ExecRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ExecResponse{Error: "Invalid JSON", ExitCode: 1})
		return
	}

	if req.Command == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ExecResponse{Error: "Empty command", ExitCode: 1})
		return
	}

	timeout := time.Duration(req.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	output, exitCode, err := runCommand(req.Command, timeout)
	resp := ExecResponse{
		Output:   strings.TrimSpace(output),
		ExitCode: exitCode,
	}
	if err != nil {
		resp.Error = err.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ===== 发送文件到 VM =====
// 流程：
// 1. 客户端发送 {filename, chunks, total_size, is_final}
// 2. VM 端将 base64 解码并写入文件
func handleFileSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(FileSendResponse{Success: false, Error: "Method not allowed"})
		return
	}

	// Auth
	auth := r.Header.Get("Authorization")
	if auth != *password && auth != "Bearer "+*password {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(FileSendResponse{Success: false, Error: "Unauthorized"})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(FileSendResponse{Success: false, Error: "Read body failed"})
		return
	}

	var req FileSendRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(FileSendResponse{Success: false, Error: "Invalid JSON"})
		return
	}

	if req.Filename == "" || len(req.Chunks) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(FileSendResponse{Success: false, Error: "filename and chunks required"})
		return
	}

	// 组装所有 base64 chunk
	var raw []byte
	for _, chunk := range req.Chunks {
		decoded, err := base64.StdEncoding.DecodeString(chunk)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(FileSendResponse{Success: false, Error: "chunk base64 decode failed: " + err.Error()})
			return
		}
		raw = append(raw, decoded...)
	}

	// 拼接所有 chunk
	fileData := raw

	// 验证大小
	if int64(len(fileData)) != req.TotalSize {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(FileSendResponse{Success: false, Error: fmt.Sprintf("size mismatch: expect %d, got %d", req.TotalSize, len(fileData))})
		return
	}

	// 确保目录存在
	dir := filepath.Dir(req.Filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(FileSendResponse{Success: false, Error: "mkdir failed: " + err.Error()})
		return
	}

	// 写入文件
	if err := os.WriteFile(req.Filename, fileData, 0644); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(FileSendResponse{Success: false, Error: "write file failed: " + err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(FileSendResponse{
		Success: true,
		Message: fmt.Sprintf("File '%s' saved successfully (%d bytes)", req.Filename, len(fileData)),
	})
}

// ===== 从 VM 接收文件 =====
// 流程：
// 1. 客户端发送 {filename}
// 2. VM 端读取文件，base64 编码，分块返回
func handleFileReceive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(FileReceiveResponse{Error: "Method not allowed"})
		return
	}

	// Auth
	auth := r.Header.Get("Authorization")
	if auth != *password && auth != "Bearer "+*password {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(FileReceiveResponse{Error: "Unauthorized"})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(FileReceiveResponse{Error: "Read body failed"})
		return
	}

	var req FileReceiveRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(FileReceiveResponse{Error: "Invalid JSON"})
		return
	}

	if req.Filename == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(FileReceiveResponse{Error: "filename required"})
		return
	}

	// 读取文件
	fileData, err := os.ReadFile(req.Filename)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(FileReceiveResponse{Error: "read file failed: " + err.Error()})
		return
	}

	if len(fileData) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(FileReceiveResponse{Error: "file is empty"})
		return
	}

	// 检查文件大小（防止过大文件撑爆内存，限制 10MB）
	const maxSize = 10 * 1024 * 1024
	if int64(len(fileData)) > maxSize {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(FileReceiveResponse{
			Error: fmt.Sprintf("file too large: %d bytes (max %d)", len(fileData), maxSize),
		})
		return
	}

	// 分割成 chunks（每个 chunk 最大 64KB，避免 base64 膨胀）
	const chunkSize = 64 * 1024
	var chunks []struct {
		Index int    `json:"index"`
		Chunk string `json:"chunk"` // base64 编码
	}

	for i := 0; i < len(fileData); i += chunkSize {
		end := i + chunkSize
		if end > len(fileData) {
			end = len(fileData)
		}
		chunks = append(chunks, struct {
			Index int    `json:"index"`
			Chunk string `json:"chunk"`
		}{
			Index: len(chunks),
			Chunk: base64.StdEncoding.EncodeToString(fileData[i:end]),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(FileReceiveResponse{
		Filename: req.Filename,
		Size:     int64(len(fileData)),
		Chunks:   chunks,
	})
}

// ===== 命令执行辅助 =====

func runCommand(cmdStr string, timeout time.Duration) (string, int, error) {
	cmd := exec.Command("cmd.exe", "/c", cmdStr)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Start()
	if err != nil {
		return "", 1, fmt.Errorf("start failed: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return stdout.String() + stderr.String(), exitErr.ExitCode(), nil
			}
			return stdout.String() + stderr.String(), 1, err
		}
		return stdout.String() + stderr.String(), 0, nil
	case <-time.After(timeout):
		cmd.Process.Kill()
		return stdout.String() + stderr.String(), 124, fmt.Errorf("timed out after %s", timeout)
	}
}