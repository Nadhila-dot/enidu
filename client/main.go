package main

import (
	"embed"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"os"
    "path/filepath"
	"bufio"
    "strings"
    "strconv"
	"net"
)

//go:embed index.html
var content embed.FS

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default: 
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Println("Failed to open browser:", err)
	}
}

func main() {
    port := GetContainerPort()

    url := "http://localhost:" + port + "/index.html?nadhi.dev=v1&compilation=go&platform=" + runtime.GOOS + "&port=" + port + "&loader=HTML"

    fs := http.FileServer(http.FS(content))
    http.Handle("/", fs)

   
    // start http server
    println("Starting Enidu Desktop" + runtime.GOOS)
    println("Copyright Nadhi.dev (2025-present)")
    println("Chamithu has a bi")
    fmt.Println("Serving app at:", url)
    if err := http.ListenAndServe(":"+port, nil); err != nil {
        panic(err)
    }
    //open in webview 
     go func() {
        openBrowser(url)
    }()
}


func GetContainerPort() string {
    // 1. Check for a file ending with .port.debug
    files, _ := filepath.Glob("*.port.debug")
    if len(files) > 0 {
        f, err := os.Open(files[0])
        if err == nil {
            defer f.Close()
            scanner := bufio.NewScanner(f)
            if scanner.Scan() {
                port := strings.TrimSpace(scanner.Text())
                if port != "" {
                    return port
                }
            }
        }
    }

    // In testing make it load the port from .env
	// This is useful for local development
    if _, err := os.Stat(".env"); err == nil {
        f, err := os.Open(".env")
        if err == nil {
            defer f.Close()
            scanner := bufio.NewScanner(f)
            for scanner.Scan() {
                line := scanner.Text()
                if strings.HasPrefix(line, "PORT=") {
                    os.Setenv("PORT", strings.TrimSpace(strings.TrimPrefix(line, "PORT=")))
                }
            }
        }
    }
    port := os.Getenv("PORT")
    if port == "" {
        // 3. fallback: find a free port
        l, err := net.Listen("tcp", ":0")
        if err != nil {
            port = "8080"
        } else {
            defer l.Close()
            port = strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
        }
    }
    // 4. Create debug file
	// removed this cuz it's stupid
   debugFile := fmt.Sprintf("port-%s.debug", port)
    _ = os.WriteFile(debugFile, []byte(port), 0644)
    return port
}