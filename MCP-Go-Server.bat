@echo off
cd /d "C:\Temp\mcp-go-server"
"C:\Temp\mcp-go-server\mcp-go-server.exe" --port 8080 --password "vmware123" --mcp-url "http://192.168.0.10:8082" --mcp-password "vmware123" --service --auto
