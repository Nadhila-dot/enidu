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

// Shutdown phases for controlled resource reduction
const (
	PhaseRunning = iota
	PhaseShuttingDown
	PhaseDraining
	PhaseStopped
)

// Global shutdown state
var (
	stopFlag     int32 = 0
	shutdownPhase int32 = PhaseRunning
	shutdownStartTime time.Time
)

// HttpTester: EXTREME THROUGHPUT MODE with Aggressive Shutdown
func HttpTester(targetURL, proxyAddr string, concurrency int, timeoutSec int64, waitMs int, randomHeaders bool) {
	// Reset all flags at the beginning
	atomic.StoreInt32(&stopFlag, 0)
	atomic.StoreInt32(&shutdownPhase, PhaseRunning)

	var sent, success, errors, timeouts, connErrors int64
	var mu sync.Mutex
	errorChan := make(chan string, 10000)

	// Allow GC to be more aggressive when needed
	debug.SetGCPercent(20)

	// Create a main context for coordinated shutdown
	mainCtx, mainCancel := context.WithCancel(context.Background())
	defer mainCancel()

	// Set up OS signal handling
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// Maximize CPU usage during normal operation
	runtime.GOMAXPROCS(runtime.NumCPU() * 2)

	// Prepare URL
	parsedURL, _ := url.Parse(targetURL)
	host := parsedURL.Host

	// Create hyper-optimized transport with enormous connection pool
	dialer := &net.Dialer{
		Timeout:   2 * time.Second,
		KeepAlive: 60 * time.Second,
		DualStack: true,
		Control: func(network, address string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
				syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1)
				syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 1048576)
				syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, 1048576)
			})
		},
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		MaxIdleConns:          5000000,
		MaxIdleConnsPerHost:   5000000,
		MaxConnsPerHost:       5000000,
		IdleConnTimeout:       90 * time.Second,
		DisableKeepAlives:     false,
		ForceAttemptHTTP2:     false,
		DisableCompression:    true,
		TLSHandshakeTimeout:   3 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: time.Duration(timeoutSec) * time.Second,
		ReadBufferSize:        1024 * 1024,
		WriteBufferSize:       1024 * 1024,
	}

	if proxyAddr != "" {
		proxyURL, _ := url.Parse(proxyAddr)
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	// Create HTTP clients
	numClients := runtime.NumCPU() * 8
	clients := make([]*http.Client, numClients)
	for i := 0; i < numClients; i++ {
		clients[i] = &http.Client{
			Transport: transport,
			Timeout:   time.Duration(timeoutSec) * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}

	// Pre-build request template
	reqTemplate, _ := http.NewRequest("GET", targetURL, nil)
	reqTemplate.Header.Set("User-Agent", userAgents[0])
	reqTemplate.Header.Set("Connection", "keep-alive")
	reqTemplate.Header.Set("Host", host)
	reqTemplate.Header.Set("Accept", "*/*")

	// Pre-create worker buffers
	bufPool := sync.Pool{
		New: func() interface{} {
			return make([]byte, 8192)
		},
	}

	// Enhanced shutdown coordinator with EXTREME memory cleanup
	stopChan := make(chan struct{})
	go func() {
		defer close(stopChan)
		
		ticker := time.NewTicker(50 * time.Millisecond) // More frequent checking
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if shouldStop() {
					fmt.Fprintf(printfWriter, "\n🛑 .stop-runner detected, initiating EXTREME MEMORY CLEANUP...\n")
					initiateShutdown(transport, clients)
					retur÷n
				}
			case <-sig:
				fmt.Fprintf(printfWriter, "\n🛑 Interrupt signal detected, initiating EXTREME MEMORY CLEANUP...\n")
				initiateShutdown(transport, clients)
				return
			case <-mainCtx.Done():
				return
			}
		}
	}()

	// AGGRESSIVE memory cleanup monitor - runs every 100ms during shutdown
	go func() {
		for {
			select {
			case <-stopChan:
				return
			case <-time.After(100 * time.Millisecond):
				phase := atomic.LoadInt32(&shutdownPhase)
				if phase >= PhaseShuttingDown {
					performAggressiveMemoryCleanup(&bufPool)
				}
			}
		}
	}()

	// Error printer goroutine with shutdown awareness
	errorPrinterDone := make(chan struct{})
	go func() {
		defer close(errorPrinterDone)
		for {
			select {
			case err, ok := <-errorChan:
				if !ok {
					return
				}
				if atomic.LoadInt32(&shutdownPhase) >= PhaseDraining {
					continue // Skip printing errors during shutdown
				}
				mu.Lock()
				fmt.Fprintf(printfWriter, "\nERROR: %s", err)
				mu.Unlock()
			case <-stopChan:
				// Drain remaining errors quickly
				for len(errorChan) > 0 {
					<-errorChan
				}
				return
			}
		}
	}()

	// Enhanced stats printer with shutdown awareness
	statsPrinterDone := make(chan struct{})
	go func() {
		defer close(statsPrinterDone)
		var lastSent, lastSuccess, lastErrors int64
		startTime := time.Now()
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				phase := atomic.LoadInt32(&shutdownPhase)
				if phase >= PhaseStopped {
					return
				}

				curSent := atomic.LoadInt64(&sent)
				curSuccess := atomic.LoadInt64(&success)
				curErrors := atomic.LoadInt64(&errors)
				curTimeouts := atomic.LoadInt64(&timeouts)
				curConnErrors := atomic.LoadInt64(&connErrors)

				duration := time.Since(startTime).Seconds()
				overallRPS := float64(curSent) / duration

				mu.Lock()
				if phase == PhaseRunning {
					fmt.Fprintf(printfWriter, "\rRuntime: %.1fs | Reqs: %d (RPS: %d | Avg: %.1f) | Success: %d (%d/s) | Errors: %d (%d/s) | Timeouts: %d | ConnErrs: %d",
						duration,
						curSent, curSent-lastSent, overallRPS,
						curSuccess, curSuccess-lastSuccess,
						curErrors, curErrors-lastErrors,
						curTimeouts, curConnErrors)
				} else {
					fmt.Fprintf(printfWriter, "\r🔄 SHUTTING DOWN - Phase: %d | Workers stopping... | Final: Sent: %d | Success: %d | Errors: %d",
						phase, curSent, curSuccess, curErrors)
				}
				mu.Unlock()

				lastSent = curSent
				lastSuccess = curSuccess
				lastErrors = curErrors

			case <-stopChan:
				return
			}
		}
	}()

	// Create worker pools
	workersPerCPU := 15000 // Even more aggressive
	totalWorkers := runtime.NumCPU() * workersPerCPU * concurrency

	fmt.Fprintf(printfWriter, "🚀 EXTREME MODE: Launching %d workers across %d CPUs against %s...\n",
		totalWorkers, runtime.NumCPU(), targetURL)
	fmt.Fprintf(printfWriter, "⚠️ Target: %s | Concurrency: %d | Workers: %d | Timeout: %ds\n",
		targetURL, concurrency, totalWorkers, timeoutSec)

	// Pre-create worker buffers
	

	// Launch workers with context-aware shutdown
	var wg sync.WaitGroup
	wg.Add(totalWorkers)

	for i := 0; i < totalWorkers; i++ {
		go func(workerID int) {
			defer wg.Done()

			// Worker-specific context that respects main shutdown
			workerCtx, workerCancel := context.WithCancel(mainCtx)
			defer workerCancel()

			client := clients[workerID%numClients]
			buffer := bufPool.Get().([]byte)
			defer bufPool.Put(buffer)

			localRand := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))
			retryMax := 3
			backoffBase := 50 * time.Millisecond

			for {
				// Multi-level shutdown checking
				select {
				case <-workerCtx.Done():
					return
				default:
				}

				if atomic.LoadInt32(&stopFlag) == 1 {
					return
				}

				phase := atomic.LoadInt32(&shutdownPhase)
				if phase >= PhaseDraining {
					// During draining phase, workers exit immediately
					return
				}

				// Create request with context for immediate cancellation
				reqCtx, reqCancel := context.WithTimeout(workerCtx, time.Duration(timeoutSec)*time.Second)
				req := reqTemplate.Clone(reqCtx)
				
				// Apply random headers if enabled (but skip during shutdown)
				if randomHeaders && phase == PhaseRunning {
					applyRandomHeaders(req, localRand)
				}

				// Adaptive wait based on shutdown phase
				if waitMs > 0 && phase == PhaseRunning {
					jitter := float64(waitMs) * 0.2
					adjustedWait := waitMs + int(localRand.Float64()*jitter-jitter/2)
					if adjustedWait > 0 {
						select {
						case <-time.After(time.Duration(adjustedWait) * time.Millisecond):
						case <-workerCtx.Done():
							reqCancel()
							return
						}
					}
				}

				// Retry logic with context awareness
				var resp *http.Response
				var err error

				for retry := 0; retry < retryMax; retry++ {
					if atomic.LoadInt32(&stopFlag) == 1 || atomic.LoadInt32(&shutdownPhase) >= PhaseDraining {
						reqCancel()
						return
					}

					resp, err = client.Do(req)
					atomic.AddInt64(&sent, 1)

					if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
						break
					}

					if err == nil || !isConnectionError(err) || retry == retryMax-1 {
						break
					}

					// Shorter backoff during shutdown phases
					backoff := backoffBase * time.Duration(1<<retry)
					if phase != PhaseRunning {
						backoff = backoff / 4 // Much shorter during shutdown
					}
					jitter := time.Duration(localRand.Int63n(int64(backoff) / 2))
					
					select {
					case <-time.After(backoff + jitter):
					case <-workerCtx.Done():
						reqCancel()
						return
					}
				}

				reqCancel()

				// Process response with shutdown awareness
				if err != nil {
					handleError(err, workerID, localRand, errorChan, &timeouts, &connErrors, &errors)
				} else if resp != nil {
					handleResponse(resp, workerID, localRand, buffer, errorChan, &success, &errors)
				}
			}
		}(i)
	}

	// Wait for shutdown signal
	<-stopChan

	// Immediately start aggressive resource reduction
	atomic.StoreInt32(&shutdownPhase, PhaseDraining)
	
	// Cancel main context to signal all workers
	mainCancel()

	// Wait for all workers with shorter timeout for faster shutdown
	waitChan := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitChan)
	}()

	// Give workers only 2 seconds to exit (more aggressive)
	select {
	case <-waitChan:
		fmt.Fprintf(printfWriter, "\n✅ All workers exited gracefully in shutdown\n")
	case <-time.After(2 * time.Second):
		fmt.Fprintf(printfWriter, "\n⚡ Force-stopping remaining workers after timeout\n")
	}

	// Final phase - complete shutdown
	atomic.StoreInt32(&shutdownPhase, PhaseStopped)

	// Close all HTTP clients and transport aggressively
	finalCleanup(transport, clients)

	// Wait for support goroutines to finish
	close(errorChan)
	<-errorPrinterDone
	<-statsPrinterDone

	// Final GC to clean up resources
	runtime.GC()
	debug.FreeOSMemory()

	// Print final stats
	fmt.Fprintf(printfWriter, "\n\n💥 HttpTester EXTREME MODE stopped. Final stats: Sent: %d | Success: %d | Errors: %d | Timeouts: %d | ConnErrs: %d\n",
		atomic.LoadInt64(&sent), atomic.LoadInt64(&success),
		atomic.LoadInt64(&errors), atomic.LoadInt64(&timeouts),
		atomic.LoadInt64(&connErrors))
	
	fmt.Fprintf(printfWriter, "🧹 Resource cleanup completed. Shutdown took: %v\n", time.Since(shutdownStartTime))
}

// Initiate coordinated shutdown with immediate resource reduction
func initiateShutdown(transport *http.Transport, clients []*http.Client) {
	shutdownStartTime = time.Now()
	atomic.StoreInt32(&stopFlag, 1)
	atomic.StoreInt32(&shutdownPhase, PhaseShuttingDown)
	
	// Immediately start reducing resource usage
	fmt.Fprintf(printfWriter, "🔧 Reducing system resource usage...\n")
	
	// Reduce GC pressure immediately
	debug.SetGCPercent(5) // Very aggressive GC
	
	// Reduce GOMAXPROCS to minimum to lower CPU usage
	runtime.GOMAXPROCS(1)
	
	// Start closing idle connections immediately
	go func() {
		transport.CloseIdleConnections()
		for _, client := range clients {
			if t, ok := client.Transport.(*http.Transport); ok {
				t.CloseIdleConnections()
			}
		}
	}()
}

// Progressive shutdown with resource reduction
func performProgressiveShutdown() {
	// Force garbage collection to free memory
	runtime.GC()
	
	// Every few cycles, free OS memory
	if time.Since(shutdownStartTime) > 500*time.Millisecond {
		debug.FreeOSMemory()
	}
}

// Final cleanup of all resources
func finalCleanup(transport *http.Transport, clients []*http.Client) {
	fmt.Fprintf(printfWriter, "🧹 Performing final resource cleanup...\n")
	
	// Close all transports
	transport.CloseIdleConnections()
	for _, client := range clients {
		if t, ok := client.Transport.(*http.Transport); ok {
			t.CloseIdleConnections()
		}
	}
	
	// Force final garbage collection
	runtime.GC()
	debug.FreeOSMemory()
	
	// Reset GOMAXPROCS to default
	runtime.GOMAXPROCS(runtime.NumCPU())
}

// Apply random headers to request
func applyRandomHeaders(req *http.Request, localRand *rand.Rand) {
	req.Header.Set("User-Agent", userAgents[localRand.Intn(len(userAgents))])
	
	cacheValues := []string{"no-cache", "max-age=0", "no-store", "must-revalidate"}
	req.Header.Set("Cache-Control", cacheValues[localRand.Intn(len(cacheValues))])
	
	if localRand.Intn(10) > 2 {
		req.Header.Set("Referer", referers[localRand.Intn(len(referers))])
	}
	
	req.Header.Set("Accept-Language", acceptLanguages[localRand.Intn(len(acceptLanguages))])
	
	if localRand.Intn(10) > 5 {
		req.Header.Set("Sec-CH-UA", "\"Google Chrome\";v=\"115\", \"Chromium\";v=\"115\", \"Not:A-Brand\";v=\"99\"")
		req.Header.Set("Sec-CH-UA-Mobile", "?0")
		req.Header.Set("Sec-CH-UA-Platform", "\"Windows\"")
	}
	
	if localRand.Intn(10) > 7 {
		req.Header.Set(fmt.Sprintf("X-Custom-%d", localRand.Intn(999)),
			fmt.Sprintf("value-%d", localRand.Intn(99999)))
	}
	
	if localRand.Intn(10) > 8 {
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
	}
}

// Handle errors with shutdown awareness
func handleError(err error, workerID int, localRand *rand.Rand, errorChan chan<- string, timeouts, connErrors, errors *int64) {
	if os.IsTimeout(err) || strings.Contains(err.Error(), "timeout") ||
		strings.Contains(err.Error(), "deadline exceeded") {
		atomic.AddInt64(timeouts, 1)
		if atomic.LoadInt32(&shutdownPhase) == PhaseRunning && localRand.Intn(100) < 5 {
			select {
			case errorChan <- fmt.Sprintf("Worker %d: Timeout: %v\n", workerID, err):
			default:
			}
		}
	} else if isConnectionError(err) {
		atomic.AddInt64(connErrors, 1)
		if atomic.LoadInt32(&shutdownPhase) == PhaseRunning && localRand.Intn(100) < 5 {
			select {
			case errorChan <- fmt.Sprintf("Worker %d: Connection error: %v\n", workerID, err):
			default:
			}
		}
	} else {
		if atomic.LoadInt32(&shutdownPhase) == PhaseRunning {
			select {
			case errorChan <- fmt.Sprintf("Worker %d: Request failed: %v\n", workerID, err):
			default:
			}
		}
	}
	atomic.AddInt64(errors, 1)
}

// Handle HTTP responses
func handleResponse(resp *http.Response, workerID int, localRand *rand.Rand, buffer []byte, errorChan chan<- string, success, errors *int64) {
	defer resp.Body.Close()
	
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		atomic.AddInt64(success, 1)
	} else {
		atomic.AddInt64(errors, 1)
		if atomic.LoadInt32(&shutdownPhase) == PhaseRunning && localRand.Intn(100) < 20 {
			select {
			case errorChan <- fmt.Sprintf("Worker %d: HTTP %d: %s\n", workerID, resp.StatusCode, resp.Status):
			default:
			}
		}
	}
	
	// Always drain the body, but do it quickly during shutdown
	io.CopyBuffer(io.Discard, resp.Body, buffer)
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

func shouldStop() bool {
	_, err := os.Stat(filepath.Join(".", ".stop-runner"))
	return err == nil
}

// Aggressively clean up memory by draining the buffer pool and forcing GC
func performAggressiveMemoryCleanup(bufPool *sync.Pool) {
	
	for i := 0; i < 1000; i++ {
		bufPool.Get()
	}
	runtime.GC()
	debug.FreeOSMemory()
}