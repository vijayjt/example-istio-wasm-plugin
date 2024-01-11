#!/usr/bin/env bash

go mod tidy

# Build with tinygo
tinygo build -o ./custom-errors.wasm -scheduler=none -target=wasi ./main.go
#tinygo build -o dh-custom-errors.wasm -gc=custom -tags=custommalloc -target=wasi -scheduler=none main.go

# Run tests with Go so we can take advantage of full go language
go test ./... -v
