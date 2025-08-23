package router

import (
    "bufio"
    "fmt"
    "net"
    "os"
    "path/filepath"
    "strconv"
    "strings"
)

// GetContainerPort returns the port the container should listen on.
// Checks for a file ending with .port.debug and uses its content if found.
// Otherwise, uses the PORT env var if set, or finds a free port.
// Also creates a file called port-<port>.debug with the port number.
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

    // 2. Check for PORT in environment (try to load .env if present)
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
    debugFile := fmt.Sprintf("port-%s.debug", port)
    _ = os.WriteFile(debugFile, []byte(port), 0644)
    return port
}