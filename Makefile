SHELL=bash

all: clean fmt test fixture install integration

clean:
	rm -rf mocks
	rm -rf mockery/fixtures/mocks

fmt:
	go fmt ./...

lint:
	golangci-lint run

test: fixture
	go test ./...

fixture:
	mkdir -p mockery/fixtures/mocks
	mockery -print -dir mockery/fixtures -name RequesterVariadic > mockery/fixtures/mocks/requester_variadic.go

install:
	go install ./cmd/mockery

integration:
	rm -rf mocks
	${GOPATH}/bin/mockery -all -recursive -cpuprofile="mockery.prof" -dir="mockery/fixtures"
	if [ ! -d "mocks" ]; then \
		echo "No Mock Dir Created"; \
		exit 1; \
	fi
	if [ ! -f "mocks/AsyncProducer.go" ]; then \
		echo "AsyncProducer.go not created"; \
		echo 1; \
	fi