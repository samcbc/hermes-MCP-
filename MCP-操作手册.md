# MCP 操作手册

## 基本信息

| 项目 | 值 |
|------|-----|
| VM IP | `192.168.0.25` |
| MCP 端口 | `8080` |
| 认证密码 | `vmware123` |
| 服务名 | `MCPGoServer` |

## 连接方式

虚拟机运行咗一个 Go EXE (`mcp-go-server.exe`)，提供 HTTP API。所有请求都通过 POST 方法，通过 `Authorization` header 传递密码。

## 基础认证

所有请求都必须包含：
```
Authorization: vmware123
```

## 可用端点

### 1. 执行命令 (`/exec`)

**用途**：在虚拟机执行命令行命令

**请求体**：
```json
{
  "command": "ipconfig",
  "timeout": 30
}
```

**返回**：
```json
{
  "output": "... 命令输出 ...",
  "exit_code": 0
}
```

**PowerShell 示例**：
```powershell
$reqJson = @{
    command = "ipconfig"
    timeout = 30
} | ConvertTo-Json -Compress

$bytes = [System.Text.Encoding]::UTF8.GetBytes($reqJson)
$headers = @{Authorization = "vmware123"; "Content-Type" = "application/json"}

Invoke-RestMethod -Uri "http://192.168.0.25:8080/exec" -Method POST -Headers $headers -Body $bytes
```

### 2. 发送文件到 VM (`/file/send`)

**用途**：将文件从主机发送到虚拟机

**请求体**：
```json
{
  "filename": "C:\\Users\\WDKRemoteUser\\Desktop\\1.txt",
  "chunks": ["SGVsbG8="],
  "total_size": 5,
  "is_final": true
}
```

**参数说明**：
- `filename`：VM 端保存路径（注意双反斜杠）
- `chunks`：Base64 编码嘅文件内容（一个或多个 chunk）
- `total_size`：原始文件大小（字节）
- `is_final`：是否最后一个 chunk

**PowerShell 示例**：
```powershell
# 读取本地文件
$rawData = [System.IO.File]::ReadAllBytes('C:\Temp\1.txt')

# 分割成 64KB 的 chunk，逐个 Base64 编码
$chunkSize = 64 * 1024
$chunks = @()
for ($i = 0; $i -lt $rawData.Length; $i += $chunkSize) {
    $end = [Math]::Min($i + $chunkSize, $rawData.Length)
    $chunkBytes = $rawData[$i..($end - 1)]
    $chunks += [Convert]::ToBase64String($chunkBytes)
}

# 构建请求
$reqJson = @{
    filename = "C:\Users\WDKRemoteUser\Desktop\1.txt"
    chunks = $chunks
    total_size = $rawData.Length
    is_final = ($chunks.Length -eq 1)  # 只有一个 chunk 系 final
} | ConvertTo-Json -Compress

$bytes = [System.Text.Encoding]::UTF8.GetBytes($reqJson)
$headers = @{Authorization = "vmware123"; "Content-Type" = "application/json"}

# 发送文件
$result = Invoke-RestMethod -Uri "http://192.168.0.25:8080/file/send" -Method POST -Headers $headers -Body $bytes
Write-Host $result
```

### 3. 从 VM 接收文件 (`/file/receive`)

**用途**：从虚拟机下载文件到主机

**请求体**：
```json
{
  "filename": "C:\\Users\\WDKRemoteUser\\Desktop\\1.txt"
}
```

**返回**：
```json
{
  "filename": "C:\\Users\\WDKRemoteUser\\Desktop\\1.txt",
  "size": 1024,
  "chunks": [
    {"index": 0, "chunk": "SGVsbG8="},
    {"index": 1, "chunk": "SGVsbG8="}
  ]
}
```

**PowerShell 示例**：
```powershell
$reqJson = @{
    filename = "C:\Users\WDKRemoteUser\Desktop\1.txt"
} | ConvertTo-Json -Compress

$bytes = [System.Text.Encoding]::UTF8.GetBytes($reqJson)
$headers = @{Authorization = "vmware123"; "Content-Type" = "application/json"}

$response = Invoke-RestMethod -Uri "http://192.168.0.25:8080/file/receive" -Method POST -Headers $headers -Body $bytes

# 将 base64 chunks 合并解码
$allBytes = @()
foreach ($chunk in $response.chunks) {
    $allBytes += [Convert]::FromBase64String($chunk.chunk)
}

# 保存文件
[System.IO.File]::WriteAllBytes('C:\Temp\received-file.txt', $allBytes)
Write-Host "File saved: C:\Temp\received-file.txt"
```

### 4. 健康检查 (`/health`)

**用途**：检查服务状态

**请求**：
```
GET http://192.168.0.25:8080/health
```

**返回**：
```json
{
  "status": "ok",
  "mcp_connected": true,
  "features": ["exec", "file/send", "file/receive"]
}
```

## 常用命令示例

### 查看系统信息
```json
{"command": "systeminfo", "timeout": 60}
```

### 列出进程
```json
{"command": "tasklist", "timeout": 10}
```

### 查看网络配置
```json
{"command": "ipconfig /all", "timeout": 30}
```

### 创建目录
```json
{"command": "mkdir C:\\Temp\\test", "timeout": 10}
```

### 删除文件
```json
{"command": "del C:\\Temp\\test.txt", "timeout": 10}
```

### 重启服务
```json
{"command": "sc stop MCPGoServer & sc start MCPGoServer", "timeout": 30}
```

### 安装服务
```json
{"command": "cd C:\\Users\\WDKRemoteUser\\Desktop & .\\mcp-go-server.exe --install", "timeout": 30}
```

### 卸载服务
```json
{"command": "sc delete MCPGoServer", "timeout": 30}
```

### 启动浏览器
```json
{"command": "start http://www.baidu.com", "timeout": 10}
```

## 注意事项

1. **路径分隔符**：JSON 中 Windows 路径需要双反斜杠 (`\\`)
2. **Base64 编码**：确保 chunks 中嘅 base64 字符串冇换行符（`\r\n`）
3. **Chunk 大小**：建议每个 chunk 不超过 64KB
4. **超时设置**：长时间运行命令（如 systeminfo）需要设置更长 timeout
5. **服务重启**：修改服务配置后需要重启服务
6. **文件路径**：发送文件时确保 VM 端路径存在（如果目录不存在，会自动创建）

## 故障排除

### 连接失败
- 检查 VM 是否运行
- 检查防火墙是否允许 8080 端口
- 检查密码是否正确

### 文件发送失败
- 检查 base64 编码是否正确（无换行符）
- 检查 total_size 是否匹配原始文件大小
- 检查 VM 端磁盘空间是否足够

### 命令执行失败
- 检查命令语法是否正确
- 检查命令是否在 PATH 中
- 检查命令是否需要管理员权限

## 快速测试

发送测试文件到桌面：
```json
{
  "filename": "C:\\Users\\WDKRemoteUser\\Desktop\\test.txt",
  "chunks": ["SGVsbG8="],
  "total_size": 5,
  "is_final": true
}
```

预期输出：
```json
{"success": true, "message": "File 'C:\\Users\\WDKRemoteUser\\Desktop\\test.txt' saved successfully (5 bytes)"}
```