.PHONY: all tidy build build-all clean

BINARY_NAME=ssh-sites
DIST_DIR=dist

all: build

tidy:
	go mod tidy

build:
	go build -ldflags="-s -w" -o $(BINARY_NAME) main.go

# Cross-compile for Mac (Apple Silicon & Intel) and Linux (AMD64 & ARM64)
build-all: clean
	mkdir -p $(DIST_DIR)
	# macOS Apple Silicon (M1/M2/M3/M4)
	GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_NAME)-darwin-arm64 main.go
	# macOS Intel
	GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_NAME)-darwin-amd64 main.go
	# Linux 64-bit
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_NAME)-linux-amd64 main.go
	# Linux ARM64
	GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_NAME)-linux-arm64 main.go
	# Windows 64-bit
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_NAME)-windows-amd64.exe main.go

clean:
	rm -rf $(DIST_DIR) $(BINARY_NAME) $(BINARY_NAME).exe
