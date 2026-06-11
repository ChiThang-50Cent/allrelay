.PHONY: build clean test run

BINARY=allrelay-server
CMD=cmd/allrelay-server

build:
	go build -o bin/$(BINARY) ./$(CMD)

clean:
	rm -rf bin/

test:
	go test ./...

run: build
	./bin/$(BINARY) --host $(HOST)

fmt:
	go fmt ./...

vet:
	go vet ./...
