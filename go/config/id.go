package config

import (
    "crypto/rand"
    "encoding/hex"
    "os"
    "path/filepath"
)

// GenerateOrLoadInstanceID generates a unique identifier for this instance and saves/loads it from disk.
// The ID is stored in $HOME/.enidu_instance_id and will persist across reboots unless deleted manually.
func GenerateOrLoadInstanceID() (string, error) {
    home, err := os.UserHomeDir()
    if err != nil {
        home = "."
    }
    idFile := filepath.Join(home, "enidu_instance_id")

    // Try to load existing ID
    if data, err := os.ReadFile(idFile); err == nil && len(data) > 0 {
        return string(data), nil
    }

    // Generate new ID
    b := make([]byte, 16)
    _, err = rand.Read(b)
    if err != nil {
        return "", err
    }
    id := hex.EncodeToString(b)

	
    // Save to file (will persist unless deleted manually)
    _ = os.WriteFile(idFile, []byte(id), 0600)
    return id, nil
}