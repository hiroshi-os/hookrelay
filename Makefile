.PHONY: test build migrate fault fault-small fmt vet

EXE=$(shell go env GOEXE)

test:
	go test -race -count=1 ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

build:
	mkdir -p bin
	go build -o bin/hookrelay$(EXE) ./cmd/hookrelay
	go build -o bin/chaosrecv$(EXE) ./cmd/chaosrecv
	go build -o bin/faultbench$(EXE) ./cmd/faultbench

fault: build
	./bin/faultbench$(EXE) -n 10000 -restarts 5 -out bench/RESULTS.md

fault-small: build
	./bin/faultbench$(EXE) -n 500 -restarts 1 -out bench/RESULTS_SMALL.md
