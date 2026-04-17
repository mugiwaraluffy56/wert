.PHONY: build run-serve run-join tidy lint cross-build clean

BINARY := wert
VERSION := 0.1.0

build: tidy
	go build -ldflags="-s -w" -o $(BINARY) .

run-serve:
	go run . serve --name Admin --port 8080

run-join:
	go run . join --host localhost:8080 --name Dev

tidy:
	go mod tidy

lint:
	go vet ./...

# Cross-platform builds
cross-build: tidy
	GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w" -o dist/$(BINARY)-linux-amd64 .
	GOOS=linux   GOARCH=arm64 go build -ldflags="-s -w" -o dist/$(BINARY)-linux-arm64 .
	GOOS=darwin  GOARCH=amd64 go build -ldflags="-s -w" -o dist/$(BINARY)-darwin-amd64 .
	GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w" -o dist/$(BINARY)-darwin-arm64 .
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o dist/$(BINARY)-windows-amd64.exe .

clean:
	rm -f $(BINARY)
	rm -rf dist/
