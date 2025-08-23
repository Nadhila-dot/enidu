package router

import (
    "crypto/rand"
    "encoding/hex"
    "fmt"
    "os"
    "path/filepath"
    "strconv"
    "sync"
    "time"

    "github.com/gofiber/fiber/v2"
    "github.com/gofiber/fiber/v2/middleware/cors"
    "github.com/gofiber/fiber/v2/middleware/logger"
    "github.com/gofiber/fiber/v2/middleware/recover"
    "github.com/gofiber/websocket/v2"
    "nadhi.dev/enidu/v2/connector"
)

// JobStatus represents the current status of a job
type JobStatus string

const (
    StatusRunning  JobStatus = "running"
    StatusStopping JobStatus = "stopping"
    StatusComplete JobStatus = "complete"
    StatusError    JobStatus = "error"
)

// JobInfo contains information about a running job
type JobInfo struct {
    ID            string    `json:"id"`
    URL           string    `json:"url"`
    Concurrency   int       `json:"concurrency"`
    TimeoutSec    int64     `json:"timeoutSec"`
    WaitMs        int       `json:"waitMs"`
    RandomHeaders bool      `json:"randomHeaders"`
    ProxyAddr     string    `json:"proxyAddr,omitempty"`
    StartTime     time.Time `json:"startTime"`
    Status        JobStatus `json:"status"`
    Stats         JobStats  `json:"stats,omitempty"`
}

// JobStats tracks performance statistics for a job
type JobStats struct {
    Sent       int64     `json:"sent"`
    Success    int64     `json:"success"`
    Errors     int64     `json:"errors"`
    Timeouts   int64     `json:"timeouts"`
    ConnErrors int64     `json:"connErrors"`
    UpdateTime time.Time `json:"updateTime"`
}

// ChannelWriter implements io.Writer and sends logs to a channel
type ChannelWriter struct {
    ch chan string
}

func (w *ChannelWriter) Write(p []byte) (int, error) {
    w.ch <- string(p)
    return len(p), nil
}

// JobManager tracks running jobs and their log channels and stop signals
type JobManager struct {
    mu     sync.RWMutex
    jobs   map[string]chan string
    stops  map[string]chan struct{}
    info   map[string]*JobInfo
    stats  map[string]*JobStats
}

var manager = &JobManager{
    jobs:   make(map[string]chan string),
    stops:  make(map[string]chan struct{}),
    info:   make(map[string]*JobInfo),
    stats:  make(map[string]*JobStats),
}

// Generate a unique job ID
func generateJobID() string {
    b := make([]byte, 8)
    rand.Read(b)
    return hex.EncodeToString(b)
}

// Register a job and return its ID, log channel, and stop channel
func (jm *JobManager) RegisterJob(jobInfo *JobInfo) (string, chan string, chan struct{}) {
    id := generateJobID()
    logCh := make(chan string, 1000) // Increased buffer size
    stopCh := make(chan struct{})
    
    jobInfo.ID = id
    jobInfo.Status = StatusRunning
    jobInfo.StartTime = time.Now()
    
    jm.mu.Lock()
    jm.jobs[id] = logCh
    jm.stops[id] = stopCh
    jm.info[id] = jobInfo
    jm.stats[id] = &JobStats{
        UpdateTime: time.Now(),
    }
    jm.mu.Unlock()
    
    return id, logCh, stopCh
}

// Get a job channel by ID
func (jm *JobManager) GetJob(id string) (chan string, bool) {
    jm.mu.RLock()
    ch, ok := jm.jobs[id]
    jm.mu.RUnlock()
    return ch, ok
}

// Get a stop channel by ID
func (jm *JobManager) GetStop(id string) (chan struct{}, bool) {
    jm.mu.RLock()
    stop, ok := jm.stops[id]
    jm.mu.RUnlock()
    return stop, ok
}

// Get job info by ID
func (jm *JobManager) GetJobInfo(id string) (*JobInfo, bool) {
    jm.mu.RLock()
    info, ok := jm.info[id]
    jm.mu.RUnlock()
    return info, ok
}

// Update job status
func (jm *JobManager) UpdateJobStatus(id string, status JobStatus) bool {
    jm.mu.Lock()
    defer jm.mu.Unlock()
    
    info, ok := jm.info[id]
    if !ok {
        return false
    }
    
    info.Status = status
    return true
}

// Update job statistics
func (jm *JobManager) UpdateJobStats(id string, stats *JobStats) bool {
    jm.mu.Lock()
    defer jm.mu.Unlock()
    
    _, ok := jm.info[id]
    if !ok {
        return false
    }
    
    stats.UpdateTime = time.Now()
    jm.stats[id] = stats
    return true
}

// List all jobs
func (jm *JobManager) ListJobs() []*JobInfo {
    jm.mu.RLock()
    defer jm.mu.RUnlock()
    
    jobs := make([]*JobInfo, 0, len(jm.info))
    for _, info := range jm.info {
        // Add current stats to the info before returning
        if stats, ok := jm.stats[info.ID]; ok {
            info.Stats = *stats
        }
        jobs = append(jobs, info)
    }
    
    return jobs
}

// Remove a job by ID
func (jm *JobManager) RemoveJob(id string) {
    jm.mu.Lock()
    if ch, ok := jm.jobs[id]; ok {
        close(ch)
        delete(jm.jobs, id)
    }
    if _, ok := jm.stops[id]; ok {
        delete(jm.stops, id)
    }
    // Keep job info and stats for history, but mark as complete
    if info, ok := jm.info[id]; ok {
        info.Status = StatusComplete
    }
    jm.mu.Unlock()
}

// StopAllJobs stops all running jobs
func (jm *JobManager) StopAllJobs() int {
    jm.mu.RLock()
    stops := make([]chan struct{}, 0, len(jm.stops))
    ids := make([]string, 0, len(jm.stops))
    for id, stopCh := range jm.stops {
        stops = append(stops, stopCh)
        ids = append(ids, id)
    }
    jm.mu.RUnlock()
    
    count := 0
    for i, stopCh := range stops {
        select {
        case stopCh <- struct{}{}:
            jm.UpdateJobStatus(ids[i], StatusStopping)
            count++
        default:
            // Channel already closed or full
        }
    }
    
    // Create stop file to signal connector
    f, err := os.Create(".stop-runner")
    if err == nil {
        f.Close()
    }
    
    return count
}

// removeStopFile removes the stop file if it exists
func removeStopFile() {
    if _, err := os.Stat(".stop-runner"); err == nil {
        os.Remove(".stop-runner")
    }
}

// parseStatLine attempts to parse a connector output line for stats
func parseStatLine(line string) *JobStats {
    if len(line) < 10 {
        return nil
    }
    
    // This is a simple example - you'll need to adapt this to match your actual output format
    var sent, success, errors, timeouts, connErrors int64
    
    // If the line contains "Reqs:", try to parse it for stats
    if _, err := fmt.Sscanf(line, "Runtime: %f | Reqs: %d (RPS: %d | Avg: %f) | Success: %d (%d/s) | Errors: %d (%d/s) | Timeouts: %d | ConnErrs: %d",
        new(float64), &sent, new(int64), new(float64), 
        &success, new(int64), &errors, new(int64), &timeouts, &connErrors); err == nil {
        
        return &JobStats{
            Sent:       sent,
            Success:    success,
            Errors:     errors,
            Timeouts:   timeouts,
            ConnErrors: connErrors,
            UpdateTime: time.Now(),
        }
    }
    
    return nil
}

func StartWebServer() {
    // Create data directory if it doesn't exist
    os.MkdirAll("data", 0755)
    
    app := fiber.New(fiber.Config{
        ReadTimeout:  10 * time.Second,
        WriteTimeout: 10 * time.Second,
        IdleTimeout:  30 * time.Second,
    })

    app.Use(logger.New(logger.Config{
        Format: "${time} | ${status} | ${latency} | ${ip} | ${method} | ${path}\n",
    }))
    app.Use(cors.New()) // Allow cross-origin requests for API usage
    app.Use(recover.New()) // Recover from panics

    // API version prefix
    api := app.Group("/api/v1")
    
    // Root endpoint with version info
    api.Get("/", func(c *fiber.Ctx) error {
        return c.JSON(fiber.Map{
            "status":  "200",
            "message": "Enidu Stress Test API v1",
            "version": "2.1.0",
        })
    })

    // Create endpoint: starts a job and returns the job info and ws URL
    api.Post("/jobs", func(c *fiber.Ctx) error {
        // Remove any existing stop file
        removeStopFile()

        // Parse request body or query parameters
        type CreateRequest struct {
            URL           string `json:"url"`
            ProxyAddr     string `json:"proxyAddr"`
            Concurrency   int    `json:"concurrency"`
            TimeoutSec    int64  `json:"timeoutSec"`
            WaitMs        int    `json:"waitMs"`
            RandomHeaders bool   `json:"randomHeaders"`
        }
        
        req := new(CreateRequest)
        
        // Try to parse from JSON body first
        if err := c.BodyParser(req); err != nil {
            // If body parsing fails, try query parameters
            req.URL = c.Query("url")
            req.ProxyAddr = c.Query("proxyAddr", "")
            req.Concurrency, _ = strconv.Atoi(c.Query("concurrency", "100"))
            req.TimeoutSec, _ = strconv.ParseInt(c.Query("timeoutSec", "10"), 10, 64)
            req.WaitMs, _ = strconv.Atoi(c.Query("waitMs", "0"))
            req.RandomHeaders, _ = strconv.ParseBool(c.Query("randomHeaders", "false"))
        }
        
        if req.URL == "" {
            return c.Status(400).JSON(fiber.Map{
                "error": "Missing URL parameter",
                "code":  "MISSING_URL",
            })
        }
        
        // Validate parameters
        if req.Concurrency <= 0 {
            req.Concurrency = 100
        }
        if req.TimeoutSec <= 0 {
            req.TimeoutSec = 10
        }
        if req.WaitMs < 0 {
            req.WaitMs = 0
        }
        
        // Create job info
        jobInfo := &JobInfo{
            URL:           req.URL,
            Concurrency:   req.Concurrency,
            TimeoutSec:    req.TimeoutSec,
            WaitMs:        req.WaitMs,
            RandomHeaders: req.RandomHeaders,
            ProxyAddr:     req.ProxyAddr,
        }
        
        // Register job
        id, logCh, stopCh := manager.RegisterJob(jobInfo)
        writer := &ChannelWriter{ch: logCh}

        // Start HttpTester in a goroutine, redirecting fmt output to our writer
        go func() {
            orig := connector.RedirectPrintf(writer)
            defer connector.RedirectPrintf(orig)
            
            done := make(chan struct{})
            go func() {
                connector.HttpTester(
                    req.URL, 
                    req.ProxyAddr, 
                    req.Concurrency, 
                    req.TimeoutSec, 
                    req.WaitMs, 
                    req.RandomHeaders,
                )
                close(done)
            }()
            
            // Process log messages for stats extraction
            go func() {
                for msg := range logCh {
                    // Try to parse stats from the log message
                    if stats := parseStatLine(msg); stats != nil {
                        manager.UpdateJobStats(id, stats)
                    }
                }
            }()
            
            select {
            case <-stopCh:
                fmt.Fprintf(writer, "Job %s stopped by user request.\n", id)
                manager.UpdateJobStatus(id, StatusStopping)
            case <-done:
                fmt.Fprintf(writer, "Job %s completed.\n", id)
                manager.UpdateJobStatus(id, StatusComplete)
            }
            
            // Wait a moment to allow logs to flush
            time.Sleep(500 * time.Millisecond)
            manager.RemoveJob(id)
        }()

        wsURL := fmt.Sprintf("/api/v1/ws/%s", id)
        return c.Status(201).JSON(fiber.Map{
            "id":     id,
            "wsUrl":  wsURL,
            "status": "running",
            "info":   jobInfo,
        })
    })

    // Get job status endpoint
    api.Get("/jobs/:id", func(c *fiber.Ctx) error {
        id := c.Params("id")
        jobInfo, ok := manager.GetJobInfo(id)
        
        if !ok {
            return c.Status(404).JSON(fiber.Map{
                "error": "Job not found",
                "code":  "JOB_NOT_FOUND",
            })
        }
        
        return c.JSON(jobInfo)
    })
    
    // List all jobs endpoint
    api.Get("/jobs", func(c *fiber.Ctx) error {
        return c.JSON(fiber.Map{
            "jobs": manager.ListJobs(),
            "count": len(manager.ListJobs()),
        })
    })

    // Stop job endpoint
    api.Delete("/jobs/:id", func(c *fiber.Ctx) error {
        id := c.Params("id")
        stopCh, ok := manager.GetStop(id)
        
        if !ok {
            return c.Status(404).JSON(fiber.Map{
                "error": "Job not found or already stopped",
                "code":  "JOB_NOT_FOUND",
            })
        }
        
        // Signal stop
        select {
        case stopCh <- struct{}{}:
            manager.UpdateJobStatus(id, StatusStopping)
        default:
            // Channel might be full or closed
        }
        
        // Create .stop-runner file
        f, err := os.Create(".stop-runner")
        if err == nil {
            f.Close()
        }
        
        return c.JSON(fiber.Map{
            "status": "stopping",
            "id":     id,
        })
    })
    
    // Stop all jobs endpoint
    api.Delete("/jobs", func(c *fiber.Ctx) error {
        count := manager.StopAllJobs()
        
        return c.JSON(fiber.Map{
            "status": "stopping_all",
            "count":  count,
        })
    })

    // Static test page
    app.Get("/test", func(c *fiber.Ctx) error {
        return c.SendFile(filepath.Join("static", "test.html"))
    })

    // WebSocket endpoint for streaming logs with versioned path
    api.Get("/ws/:id", websocket.New(func(c *websocket.Conn) {
        id := c.Params("id")
        logCh, ok := manager.GetJob(id)
        
        if !ok {
            c.WriteJSON(fiber.Map{
                "error": "Invalid or expired job ID",
                "code":  "INVALID_JOB_ID",
            })
            c.Close()
            return
        }
        
        // Send initial info
        jobInfo, _ := manager.GetJobInfo(id)
        c.WriteJSON(fiber.Map{
            "type": "info",
            "data": jobInfo,
        })
        
        // Listen for messages
        for msg := range logCh {
            err := c.WriteJSON(fiber.Map{
                "type": "log",
                "data": msg,
                "ts":   time.Now().Unix(),
            })
            
            if err != nil {
                break
            }
        }
        
        c.Close()
    }))

    port := GetContainerPort()
    fmt.Printf("Starting Enidu API server on port %s...\n", port)
    if err := app.Listen(":" + port); err != nil {
        fmt.Printf("Webserver error: %v\n", err)
    }
}

