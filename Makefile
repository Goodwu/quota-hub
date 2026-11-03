# quota-hub：独立额度守护进程（macOS 本机）。
#   make build            # vet + test 后构建 bin/quota-hub-<version>
#   make install          # 构建 → 安装 /usr/local/bin/quota-hub + LaunchAgent 并启动
#   make uninstall        # 停止并移除 LaunchAgent 与二进制（不动 ~/.quota-hub 数据）
#   make test / race      # 单测 / race 检测
GIT_DESCRIBE := $(shell git describe --tags --match 'v[0-9]*' --dirty 2>/dev/null || git describe --always 2>/dev/null)
VERSION ?= $(if $(GIT_DESCRIBE),$(patsubst v%,%,$(GIT_DESCRIBE)),0.1.0)

BIN := bin/quota-hub
LAUNCH_AGENT_LABEL := com.quota-hub
LAUNCH_AGENT := $(HOME)/Library/LaunchAgents/$(LAUNCH_AGENT_LABEL).plist
INSTALL_BIN := /usr/local/bin/quota-hub
LOG := /tmp/quota-hub.launchd.log

.PHONY: build test race fmt vet install uninstall restart logs clean

build: vet test
	@mkdir -p bin
	go build -trimpath -ldflags "-X main.buildVersion=$(VERSION)" -o $(BIN) .
	@echo "built $(BIN) ($(VERSION))"

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race -count=2 ./...

fmt:
	gofmt -w .

install: build
	install -m 0755 $(BIN) $(INSTALL_BIN)
	mkdir -p $(HOME)/Library/LaunchAgents
	sed 's|/tmp/quota-hub.launchd.log|$(LOG)|' deploy/$(LAUNCH_AGENT_LABEL).plist > $(LAUNCH_AGENT)
	launchctl bootout gui/$$(id -u)/$(LAUNCH_AGENT_LABEL) 2>/dev/null || true
	launchctl bootstrap gui/$$(id -u) $(LAUNCH_AGENT)
	@echo "installed + started ($(LAUNCH_AGENT_LABEL)); logs: $(LOG)"

uninstall:
	launchctl bootout gui/$$(id -u)/$(LAUNCH_AGENT_LABEL) 2>/dev/null || true
	rm -f $(LAUNCH_AGENT) $(INSTALL_BIN)
	@echo "uninstalled (kept ~/.quota-hub config/key)"

restart:
	launchctl kickstart -k gui/$$(id -u)/$(LAUNCH_AGENT_LABEL)

logs:
	tail -f $(LOG)

clean:
	rm -rf bin
