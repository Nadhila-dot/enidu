package main

import (
    "fmt"
    "log"

    "nadhi.dev/enidu/v2/config"
    "nadhi.dev/enidu/v2/router"
)

func main() {
    //We will use this id later for authentication
    //but for now nahhh
    id, err := config.GenerateOrLoadInstanceID()
    if err != nil {
        log.Fatalf("Error generating or loading instance ID: %v", err)
    }
    fmt.Printf("Instance ID: %s\n", id)
    urls, err := config.ReadTypes()
    if err != nil {
        log.Fatalf("Error reading types: %v", err)
    }
    if len(urls) == 0 {
        log.Fatalf("No website URLs found in types.txt")
    }
    fmt.Println("Websites to stress test:", urls)

    
    router.StartWebServer()
}