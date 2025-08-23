package config

import (
    "io/ioutil"
    "os"
    "strings"
)

// ReadTypes reads the "types.txt" file in the current directory.
// If the file does not exist, it creates it and returns an empty slice.
// It returns a slice of website URLs (split by comma).
func ReadTypes() ([]string, error) {
    filename := "types.txt"

    // Check if file exists, create if not
    if _, err := os.Stat(filename); os.IsNotExist(err) {
        err := ioutil.WriteFile(filename, []byte(""), 0644)
        if err != nil {
            return nil, err
        }
        return []string{}, nil
    }

    data, err := ioutil.ReadFile(filename)
    if err != nil {
        return nil, err
    }

    content := strings.TrimSpace(string(data))
    if content == "" {
        return []string{}, nil
    }

    urls := strings.Split(content, ",")
    for i := range urls {
        urls[i] = strings.TrimSpace(urls[i])
    }
    return urls, nil
}

