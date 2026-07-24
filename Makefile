BINARY=health-calculator

.PHONY: build vet test lint run clean docker-build docker-run

build:
	go build -ldflags="-w -s" -o $(BINARY) .

vet:
	go vet ./...

test:
	go test -v -count=1 -race ./...

lint:
	golangci-lint run

run:
	go run .

clean:
	rm -f $(BINARY)

docker-build:
	docker build -t health-calculator .

docker-run:
	docker run -p 8080:8080 \
		-v $(PWD)/health-config.yaml:/root/health-config.yaml:ro \
		health-calculator
