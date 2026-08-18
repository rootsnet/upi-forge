# upi-forge 빌드
#
# 외부 모듈 의존이 없으므로 폐쇄망(disconnected) 환경의 bastion에서도
# `make` 한 번으로 빌드됩니다.
# Go 1.23 이상이면 됩니다.

BINARY   := upi-forge
BIN_DIR  := bin
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X upi-forge/internal/cli.Version=$(VERSION)

.PHONY: all build test vet fmt fmt-check lint clean install dist help

all: fmt-check vet test build

## build: 실행 파일을 bin/ 에 만듭니다.
build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) ./cmd/$(BINARY)
	@echo "빌드 완료: $(BIN_DIR)/$(BINARY) ($(VERSION))"

## test: 단위 테스트를 실행합니다.
test:
	go test ./...

## vet: 정적 검사를 실행합니다.
vet:
	go vet ./...

## fmt: 코드를 포맷합니다.
fmt:
	gofmt -w .

## fmt-check: 포맷이 어긋난 파일이 있으면 실패합니다.
fmt-check:
	@files=$$(gofmt -l .); \
	if [ -n "$$files" ]; then echo "gofmt 필요:"; echo "$$files"; exit 1; fi

## install: $(GOPATH)/bin 에 설치합니다.
install:
	CGO_ENABLED=0 go install -trimpath -ldflags '$(LDFLAGS)' ./cmd/$(BINARY)

## dist: 실행 파일과 설정을 묶어 배포용 tar.gz를 만듭니다.
dist: build
	@mkdir -p dist
	tar czf dist/$(BINARY)-$(VERSION)-linux-amd64.tar.gz \
		-C $(BIN_DIR) $(BINARY) \
		-C .. configs README.md
	@echo "배포 파일: dist/$(BINARY)-$(VERSION)-linux-amd64.tar.gz"

## clean: 빌드 산출물을 지웁니다.
clean:
	rm -rf $(BIN_DIR) dist

## help: 사용 가능한 타깃을 보여줍니다.
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //'
