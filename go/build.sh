#!/bin/bash

set -e

APP_NAME="enidu"

BUILD_DIR="build"

mkdir -p "$BUILD_DIR"

echo "Building for Linux (amd64)..."
GOOS=linux GOARCH=amd64 go build -o "$BUILD_DIR/${APP_NAME}-linux-amd64" ./

echo "Building for Linux (arm64)..."
GOOS=linux GOARCH=arm64 go build -o "$BUILD_DIR/${APP_NAME}-linux-arm64" ./

echo "Building for macOS (amd64)..."
GOOS=darwin GOARCH=amd64 go build -o "$BUILD_DIR/${APP_NAME}-darwin-amd64" ./

echo "Building for macOS (arm64)..."
GOOS=darwin GOARCH=arm64 go build -o "$BUILD_DIR/${APP_NAME}-darwin-arm64" ./

echo "Building for Windows (amd64)..."
GOOS=windows GOARCH=amd64 go build -o "$BUILD_DIR/${APP_NAME}-windows-amd64.exe" ./

echo "All builds complete. Binaries are in the '$BUILD_DIR' folder."