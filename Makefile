.PHONY: help build run test clean docker-build-app docker-build-docreader docker-build-frontend docker-build-all docker-run migrate-up migrate-down docker-restart docker-stop start-all stop-all start-ollama stop-ollama build-images build-images-app build-images-docreader build-images-frontend clean-images check-env list-containers pull-images show-platform dev-start dev-stop dev-restart dev-logs dev-status dev-app dev-frontend docs install-swagger build-lite run-lite package-lite experiment-check experiment-c1 experiment-c2-rules experiment-c2-batch experiment-c2-compare experiment-c3 experiment-c46 experiment-c46-negative experiment-c47 experiment-c47-negative experiment-c48 experiment-c48-negative experiment-c49 experiment-c49-review experiment-c410-plan experiment-c410-inventory experiment-c410-prepare experiment-c410-materialize experiment-c410-docreader-fixture experiment-c410-docreader-smoke experiment-c410-docreader-pdf-smoke experiment-c410-docreader-lifecycle experiment-c410-synthetic-corpus experiment-c410-fact-eval experiment-public-wikifactdiff-plan experiment-public-vitaminc-plan experiment-public-pair-eval experiment-public-pair-dry-run experiment-public-pair-resummarize experiment-public-self-test experiment-c4 experiment-c4-fuzzy experiment-c4-resolve experiment-p2 experiment-p3 experiment-p12 experiment-v1 experiment-audit experiment-audit-summary experiment-audit-metrics experiment-gold-v2 experiment-gold-v2-review experiment-gold-v2-scope-review experiment-gold-v2-apply-recommendations experiment-gold-v2-finalize experiment-dual-scope-metrics paper-figures

# Show help
help:
	@echo "WeKnora Makefile 帮助"
	@echo ""
	@echo "基础命令:"
	@echo "  build             构建应用"
	@echo "  run               运行应用"
	@echo "  test              运行测试"
	@echo "  clean             清理构建文件"
	@echo ""
	@echo "Docker 命令:"
	@echo "  docker-build-app       构建应用 Docker 镜像 (wechatopenai/weknora-app)"
	@echo "  docker-build-docreader 构建文档读取器镜像 (wechatopenai/weknora-docreader)"
	@echo "  docker-build-frontend  构建前端镜像 (wechatopenai/weknora-ui)"
	@echo "  docker-build-all       构建所有 Docker 镜像"
	@echo "  docker-run            运行 Docker 容器"
	@echo "  docker-stop           停止 Docker 容器"
	@echo "  docker-restart        重启 Docker 容器"
	@echo ""
	@echo "服务管理:"
	@echo "  start-all         启动所有服务"
	@echo "  stop-all          停止所有服务"
	@echo "  start-ollama      仅启动 Ollama 服务"
	@echo ""
	@echo "镜像构建:"
	@echo "  build-images      从源码构建所有镜像"
	@echo "  build-images-app  从源码构建应用镜像"
	@echo "  build-images-docreader 从源码构建文档读取器镜像"
	@echo "  build-images-frontend  从源码构建前端镜像"
	@echo "  clean-images      清理本地镜像"
	@echo ""
	@echo "数据库:"
	@echo "  migrate-up        执行数据库迁移"
	@echo "  migrate-down      回滚数据库迁移"
	@echo ""
	@echo "开发工具:"
	@echo "  fmt               格式化代码"
	@echo "  lint              代码检查"
	@echo "  deps              安装依赖"
	@echo "  docs              生成 Swagger API 文档"
	@echo "  install-swagger   安装 swag 工具"
	@echo ""
	@echo "环境检查:"
	@echo "  check-env         检查环境配置"
	@echo "  list-containers   列出运行中的容器"
	@echo "  pull-images       拉取最新镜像"
	@echo "  show-platform     显示当前构建平台"
	@echo ""
	@echo "开发模式（推荐）:"
	@echo "  dev-start         启动开发环境基础设施（仅启动依赖服务）"
	@echo "                    可选: make dev-start DEV_ARGS=--odl-hybrid"
	@echo "  dev-stop          停止开发环境"
	@echo "  dev-restart       重启开发环境"
	@echo "  dev-logs          查看开发环境日志"
	@echo "  dev-status        查看开发环境状态"
	@echo "  dev-app           启动后端应用（本地运行，需先运行 dev-start）"
	@echo "  dev-frontend      启动前端（本地运行，需先运行 dev-start）"
	@echo ""
	@echo "研究实验（脚本化，无需 UI）:"
	@echo "  experiment-check  检查 C1.5 实验 API/数据库导出环境"
	@echo "  experiment-c1     运行六文档 C1 生产模型实验"
	@echo "  experiment-c2-rules 运行 C2-A 规则层消融实验"
	@echo "  experiment-c2-batch 运行 C2-B 规则层 + 批量 LLM 实验"
	@echo "  experiment-c2-compare 对比显式指定的 V1/C1/C2 运行产物（RUNS=...）"
	@echo "  experiment-c3     运行 C3-Lite 版本/发布机构建议实验"
	@echo "  experiment-c46    运行 C3/C4.6 三来源全局胜方 proposal 实验"
	@echo "  experiment-c46-negative 运行 C3/C4.6 跨发布机构无 winner proposal 回归"
	@echo "  experiment-c47    显式采纳一个 fresh C4.6 winner proposal（RUN=<run>）"
	@echo "  experiment-c47-negative 验证无 proposal 时 API 拒绝采纳（RUN=<run>）"
	@echo "  experiment-c48    显式 reopen 一个 fresh C4.7 adoption（RUN=<run>）"
	@echo "  experiment-c48-negative 验证无 active adoption 时 API 拒绝 reopen（RUN=<run>）"
	@echo "  experiment-c49    运行 C4.6/C4.7/C4.8 多 replicate 生命周期矩阵（REPLICATES=3）"
	@echo "  experiment-c49-review 汇总 C4.9 双审阅 CSV（REVIEW=<csv>）"
	@echo "  experiment-c410-plan 验证真实语料 split 并生成 C4.9 matrix（CORPUS=<json>）"
	@echo "  experiment-c410-inventory 扫描私有文档文件夹并生成分组候选（DOC_ROOT=<dir>）"
	@echo "  experiment-c410-prepare 从 inventory 去重/过滤生成可编辑选材表（INVENTORY=<csv>）"
	@echo "  experiment-c410-materialize 将人工确认选材表转为 corpus JSON（SELECTION=<csv> CORPUS=<json>）"
	@echo "  experiment-c410-docreader-fixture 校验内置 synthetic DOCX/PDF fixture"
	@echo "  experiment-c410-docreader-smoke 运行内置 3 文件 DOCX fact/winner smoke"
	@echo "  experiment-c410-docreader-pdf-smoke 运行内置单 PDF parse/claim smoke"
	@echo "  experiment-c410-docreader-lifecycle 运行内置 DOCX C4.6/C4.7/C4.8 matrix"
	@echo "  experiment-c410-synthetic-corpus 生成 split-safe synthetic DOCX policy corpus（OUTPUT=<dir>）"
	@echo "  experiment-c410-fact-eval 汇总 matrix 的事实家族 winner/baseline 指标（MATRIX_RUN=<dir>）"
	@echo "  experiment-public-wikifactdiff-plan 生成公开 WikiFactDiff 评测计划（PUBLIC_OUTPUT=<dir>）"
	@echo "  experiment-public-vitaminc-plan 生成公开 VitaminC real-split 评测计划（PUBLIC_OUTPUT=<dir>）"
	@echo "  experiment-public-pair-eval 运行已生成的公开 pair manifest（PUBLIC_MANIFEST=<json>）"
	@echo "  experiment-public-pair-dry-run 校验公开 pair manifest，不访问服务（PUBLIC_MANIFEST=<json>）"
	@echo "  experiment-public-pair-resummarize 只读重汇总已完成 public run（PUBLIC_RUN=<dir>）"
	@echo "  experiment-public-self-test 离线校验公开数据适配、split 和评分完整性"
	@echo "  experiment-c4     运行 C4-Lite 三值同事实聚类实验"
	@echo "  experiment-c4-fuzzy 运行 C4-Lite schema-drift fallback 聚类实验"
	@echo "  experiment-c4-resolve 对一个 C4 cluster 执行安全传播裁决（RUN=...）"
	@echo "  experiment-p2     运行 P2 claim→detect 时序隔离实验"
	@echo "  experiment-p3     运行 P3 fallback 隔离回归实验"
	@echo "  experiment-p12    运行 doc1/doc2/doc5 全上下文 P1/P2 诊断实验"
	@echo "  experiment-v1     运行关闭 claims 的 V1 消融对照"
	@echo "  experiment-audit  导出某次完整 run 的 C1 人工审计包（RUN=<run目录>）"
	@echo "  experiment-audit-summary 汇总人工标注审计表（AUDIT=<claim_audit目录>）"
	@echo "  experiment-audit-metrics 计算人工校正指标（AUDIT_CSV=... SEMANTIC_REVIEW=...）"
	@echo "  experiment-gold-v2       生成待复核 gold-v2 候选集（ADDITIONS=... OUTPUT=...）"
	@echo "  experiment-gold-v2-review 生成 gold-v2 quote 补全表（ADDITIONS=... REVIEW=...）"
	@echo "  experiment-gold-v2-scope-review 生成 broad/narrow scope 审核表（CANDIDATE=... REVIEW=...）"
	@echo "  experiment-gold-v2-apply-recommendations 应用版本化 dual-scope 推荐（REVIEW=... OUTPUT=...）"
	@echo "  experiment-gold-v2-finalize 生成最终 broad candidate 与 narrow manifest"
	@echo "  experiment-dual-scope-metrics 计算 scope/dedup 后的 broad/narrow 指标"
	@echo ""
	@echo "论文素材（无需服务）:"
	@echo "  paper-figures    生成 Conflict V2 可编辑 SVG 图表（FIGURE_OUTPUT=<dir>）"
	@echo ""
	@echo "Lite 模式（零外部依赖）:"
	@echo "  build-lite        构建 Lite 版本（先构建前端到 web/，再构建 Go；SKIP_FRONTEND=1 跳过前端）"
	@echo "  run-lite          构建并启动 Lite 版本"
	@echo "  package-lite      构建并打包 Lite 发行包（tarball）"
	@echo "  package-mac-app   构建并打包 macOS 桌面应用 (.app)"

# Go related variables
BINARY_NAME=WeKnora
MAIN_PATH=./cmd/server

# Docker related variables
DOCKER_IMAGE=wechatopenai/weknora-app
DOCKER_TAG=latest

# Platform detection
ifeq ($(shell uname -m),x86_64)
    PLATFORM=linux/amd64
else ifeq ($(shell uname -m),aarch64)
    PLATFORM=linux/arm64
else ifeq ($(shell uname -m),arm64)
    PLATFORM=linux/arm64
else
    PLATFORM=linux/amd64
endif

# Build the application
build:
	go build -o $(BINARY_NAME) $(MAIN_PATH)

# Run the application
run: build
	./$(BINARY_NAME)

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	go clean
	rm -f $(BINARY_NAME)

# Build Docker image
docker-build-app:
	@echo "获取版本信息..."
	@eval $$(./scripts/get_version.sh env); \
	./scripts/get_version.sh info; \
	docker build --platform $(PLATFORM) \
		--build-arg VERSION_ARG="$$VERSION" \
		--build-arg COMMIT_ID_ARG="$$COMMIT_ID" \
		--build-arg BUILD_TIME_ARG="$$BUILD_TIME" \
		--build-arg GO_VERSION_ARG="$$GO_VERSION" \
		-f docker/Dockerfile.app -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

# Build docreader Docker image
docker-build-docreader:
	docker build --platform $(PLATFORM) -f docker/Dockerfile.docreader -t wechatopenai/weknora-docreader:latest .

# Build frontend Docker image
docker-build-frontend:
	./scripts/build_frontend_dist.sh
	docker build --platform $(PLATFORM) -f frontend/Dockerfile -t wechatopenai/weknora-ui:latest frontend/

# Build all Docker images
docker-build-all: docker-build-app docker-build-docreader docker-build-frontend

# Run Docker container (传统方式)
# Touch .env if missing — docker-compose.yml's `env_file: [.env]` is required
# for ${ENV} interpolation in builtin_models.yaml and would otherwise refuse
# to parse on fresh clones. `start-all` handles this via check_env_file; this
# direct path needs its own guard.
docker-run:
	@[ -f .env ] || ([ -f .env.example ] && cp .env.example .env || touch .env)
	docker-compose up

# 使用新脚本启动所有服务
start-all:
	./scripts/start_all.sh

# 使用新脚本仅启动Ollama服务
start-ollama:
	./scripts/start_all.sh --ollama

# 使用新脚本仅启动Docker容器
start-docker:
	./scripts/start_all.sh --docker

# 使用新脚本停止所有服务
stop-all:
	./scripts/start_all.sh --stop

# Stop Docker container (传统方式)
docker-stop:
	docker-compose down

# 从源码构建镜像相关命令
build-images:
	./scripts/build_images.sh

build-images-app:
	./scripts/build_images.sh --app

build-images-docreader:
	./scripts/build_images.sh --docreader

build-images-frontend:
	./scripts/build_images.sh --frontend

clean-images:
	./scripts/build_images.sh --clean

# Restart Docker container (stop, start)
docker-restart:
	@[ -f .env ] || ([ -f .env.example ] && cp .env.example .env || touch .env)
	docker-compose stop -t 60
	docker-compose up

# Database migrations
migrate-up:
	./scripts/migrate.sh up

migrate-down:
	./scripts/migrate.sh down

migrate-version:
	./scripts/migrate.sh version

migrate-create:
	@if [ -z "$(name)" ]; then \
		echo "Error: migration name is required"; \
		echo "Usage: make migrate-create name=your_migration_name"; \
		exit 1; \
	fi
	./scripts/migrate.sh create $(name)

migrate-force:
	@if [ -z "$(version)" ]; then \
		echo "Error: version is required"; \
		echo "Usage: make migrate-force version=4"; \
		exit 1; \
	fi
	./scripts/migrate.sh force $(version)

migrate-goto:
	@if [ -z "$(version)" ]; then \
		echo "Error: version is required"; \
		echo "Usage: make migrate-goto version=3"; \
		exit 1; \
	fi
	./scripts/migrate.sh goto $(version)

# Generate API documentation (Swagger)
docs:
	@echo "生成 Swagger API 文档..."
	swag init -g $(MAIN_PATH)/main.go -o ./docs --parseDependency --parseInternal
	@echo "文档已生成到 ./docs 目录"
	@echo "启动服务后访问 http://localhost:8080/swagger/index.html 查看文档"

# Install swagger tool
install-swagger:
	go install github.com/swaggo/swag/cmd/swag@latest

# Format code
fmt:
	go fmt ./...

# Lint code
lint:
	golangci-lint run

# Install dependencies
deps:
	go mod download

# Build for production
# google.golang.org/protobuf/reflect/protoregistry.conflictPolicy=warn for qdrant milvus proto conflict
build-prod:
	VERSION=$$(git describe --tags --abbrev=0 2>/dev/null || echo "$${VERSION:-unknown}"); \
	COMMIT_ID=$${COMMIT_ID:-unknown}; \
	CGO_ENABLED=1 \
	CGO_CFLAGS="-Wno-deprecated-declarations" \
	CGO_LDFLAGS="$$(if [ "$$(uname)" = 'Darwin' ]; then echo '-Wl,-no_warn_duplicate_libraries'; fi)" \
	BUILD_TIME=$${BUILD_TIME:-unknown}; \
	GO_VERSION=$${GO_VERSION:-unknown}; \
	LDFLAGS="-X 'github.com/Tencent/WeKnora/internal/handler.Version=$$VERSION' -X 'github.com/Tencent/WeKnora/internal/handler.Edition=standard' -X 'github.com/Tencent/WeKnora/internal/handler.CommitID=$$COMMIT_ID' -X 'github.com/Tencent/WeKnora/internal/handler.BuildTime=$$BUILD_TIME' -X 'github.com/Tencent/WeKnora/internal/handler.GoVersion=$$GO_VERSION' -X 'google.golang.org/protobuf/reflect/protoregistry.conflictPolicy=warn'"; \
	go build -ldflags="-w -s $$LDFLAGS" -o $(BINARY_NAME) $(MAIN_PATH)

# Build Lite version (single binary, SQLite + in-memory queue)
# 会先构建前端到 web/，再构建 Go 二进制；SKIP_FRONTEND=1 可跳过前端
# Usage: make paper-figures FIGURE_OUTPUT=$HOME/weknora-paper-assets/conflict-v2 [OVERWRITE=1]
paper-figures:
	@test -n "$(FIGURE_OUTPUT)" || (echo "Usage: make paper-figures FIGURE_OUTPUT=$$HOME/weknora-paper-assets/conflict-v2"; exit 2)
	python3 scripts/paper_figures/generate_conflict_v2_figures.py --output-dir "$(FIGURE_OUTPUT)" $(if $(OVERWRITE),--overwrite)

build-lite:
	@if [ -f frontend/package.json ] && [ "$${SKIP_FRONTEND:-}" != "1" ]; then \
		echo ">> Building frontend for Lite..."; \
		(cd frontend && npm ci --prefer-offline && npm run build) && \
		rm -rf web && cp -r frontend/dist web; \
	elif [ "$${SKIP_FRONTEND:-}" = "1" ]; then \
		echo ">> Skipping frontend (SKIP_FRONTEND=1)"; \
	else \
		echo ">> No frontend/package.json, skipping frontend"; \
	fi
	export EDITION=lite; \
	eval "$$(./scripts/get_version.sh env)"; \
	LDFLAGS="$$(./scripts/get_version.sh ldflags) -X 'google.golang.org/protobuf/reflect/protoregistry.conflictPolicy=warn'"; \
	CGO_ENABLED=1 \
	CGO_CFLAGS="-Wno-deprecated-declarations" \
	CGO_LDFLAGS="$$(if [ "$$(uname)" = 'Darwin' ]; then echo '-Wl,-no_warn_duplicate_libraries'; fi)" \
	go build -tags "sqlite_fts5" -ldflags="-w -s $$LDFLAGS" -o $(BINARY_NAME)-lite $(MAIN_PATH)

# Run Lite version with .env.lite defaults
run-lite: build-lite
	@if [ ! -f .env.lite ]; then echo "Error: .env.lite not found"; exit 1; fi
	@set -a && . ./.env.lite && set +a && ./$(BINARY_NAME)-lite

# Package Lite version into distributable tarball
package-lite:
	./scripts/package-lite.sh

# Package Mac App
package-mac-app:
	./scripts/package-mac-app.sh

download_spatial:
	go run cmd/download/duckdb/duckdb.go

clean-db:
	@echo "Cleaning database..."
	@if [ $$(docker volume ls -q -f name=weknora_postgres-data) ]; then \
		docker volume rm weknora_postgres-data; \
	fi
	@if [ $$(docker volume ls -q -f name=weknora_minio_data) ]; then \
		docker volume rm weknora_minio_data; \
	fi
	@if [ $$(docker volume ls -q -f name=weknora_redis_data) ]; then \
		docker volume rm weknora_redis_data; \
	fi

# Environment check
check-env:
	./scripts/start_all.sh --check

# List containers
list-containers:
	./scripts/start_all.sh --list

# Pull latest images
pull-images:
	./scripts/start_all.sh --pull

# Show current platform
show-platform:
	@echo "当前系统架构: $(shell uname -m)"
	@echo "Docker构建平台: $(PLATFORM)"

# Development mode commands
dev-start:
	./scripts/dev.sh start $(DEV_ARGS)

dev-stop:
	./scripts/dev.sh stop

dev-restart:
	./scripts/dev.sh restart

dev-logs:
	./scripts/dev.sh logs

dev-status:
	./scripts/dev.sh status

dev-app:
	./scripts/dev.sh app

dev-frontend:
	./scripts/dev.sh frontend

# Research experiment runner (C1.5). The app and dev infrastructure must
# already be running; credentials/configuration are taken from environment
# variables documented in scripts/experiments/README.md.
experiment-check:
	python3 scripts/experiments/run_claims_eval.py --check --check-db

experiment-c1:
	python3 scripts/experiments/run_claims_eval.py \
		--scenario scripts/experiments/scenarios/c1_full.json --variant c1

experiment-c2-rules:
	python3 scripts/experiments/run_claims_eval.py \
		--scenario scripts/experiments/scenarios/c1_full.json --variant c2-rules

experiment-c2-batch:
	python3 scripts/experiments/run_claims_eval.py \
		--scenario scripts/experiments/scenarios/c1_full.json --variant c2-batch

# Usage: make experiment-c2-compare RUNS='experiments/runs/<v1> experiments/runs/<c1> experiments/runs/<c2-rules> experiments/runs/<c2-batch>' [BASELINE=c1] [OUTPUT=experiments/comparisons/<name>]
experiment-c2-compare:
	@test -n "$(RUNS)" || (echo "Usage: make experiment-c2-compare RUNS='experiments/runs/<v1> experiments/runs/<c1> experiments/runs/<c2-rules> experiments/runs/<c2-batch>'"; exit 2)
	python3 scripts/experiments/compare_conflict_runs.py \
		$(foreach run,$(RUNS),--run "$(run)") \
		$(if $(BASELINE),--baseline "$(BASELINE)") \
		$(if $(OUTPUT),--output-dir "$(OUTPUT)")

experiment-c3:
	python3 scripts/experiments/run_claims_eval.py \
		--scenario scripts/experiments/scenarios/c3_version_suggestion.json --variant c2-rules

experiment-c46:
	python3 scripts/experiments/run_claims_eval.py \
		--scenario scripts/experiments/scenarios/c46_global_winner_triplet.json --variant c2-rules

experiment-c46-negative:
	python3 scripts/experiments/run_claims_eval.py \
		--scenario scripts/experiments/scenarios/c46_cross_issuer_no_proposal.json --variant c2-rules

# Usage: make experiment-c47 RUN=experiments/runs/<fresh-c46-positive-run> [CLUSTER_ID=<id>]
experiment-c47:
	@test -n "$(RUN)" || (echo "Usage: make experiment-c47 RUN=experiments/runs/<fresh-c46-positive-run>"; exit 2)
	python3 scripts/experiments/run_winner_adoption.py --run-dir "$(RUN)" $(if $(CLUSTER_ID),--cluster-id "$(CLUSTER_ID)")

# Usage: make experiment-c47-negative RUN=experiments/runs/<fresh-c46-negative-run> [CLUSTER_ID=<id>]
experiment-c47-negative:
	@test -n "$(RUN)" || (echo "Usage: make experiment-c47-negative RUN=experiments/runs/<fresh-c46-negative-run>"; exit 2)
	python3 scripts/experiments/run_winner_adoption.py --run-dir "$(RUN)" --expect-no-proposal $(if $(CLUSTER_ID),--cluster-id "$(CLUSTER_ID)")

# Usage: make experiment-c48 RUN=experiments/runs/<fresh-c46-positive-run-already-used-by-c47> [CLUSTER_ID=<id>]
experiment-c48:
	@test -n "$(RUN)" || (echo "Usage: make experiment-c48 RUN=experiments/runs/<fresh-c46-positive-run-already-used-by-c47>"; exit 2)
	python3 scripts/experiments/run_winner_reopen.py --run-dir "$(RUN)" $(if $(CLUSTER_ID),--cluster-id "$(CLUSTER_ID)")

# Usage: make experiment-c48-negative RUN=experiments/runs/<fresh-c46-negative-run> [CLUSTER_ID=<id>]
experiment-c48-negative:
	@test -n "$(RUN)" || (echo "Usage: make experiment-c48-negative RUN=experiments/runs/<fresh-c46-negative-run>"; exit 2)
	python3 scripts/experiments/run_winner_reopen.py --run-dir "$(RUN)" --expect-no-active-adoption $(if $(CLUSTER_ID),--cluster-id "$(CLUSTER_ID)")

# Usage: make experiment-c49 [REPLICATES=3] [MATRIX=scripts/experiments/scenarios/c49_winner_lifecycle_matrix.json] [OUTPUT=experiments/comparisons/<run>]
experiment-c49:
	python3 scripts/experiments/run_winner_lifecycle_eval.py --replicates "$(if $(REPLICATES),$(REPLICATES),3)" $(if $(MATRIX),--matrix "$(MATRIX)") $(if $(OUTPUT),--output-dir "$(OUTPUT)")

# Usage: make experiment-c49-review REVIEW=experiments/comparisons/<c49-run>/winner_lifecycle_review.csv [OUTPUT=...]
experiment-c49-review:
	@test -n "$(REVIEW)" || (echo "Usage: make experiment-c49-review REVIEW=experiments/comparisons/<c49-run>/winner_lifecycle_review.csv"; exit 2)
	python3 scripts/experiments/summarize_winner_lifecycle_review.py --review "$(REVIEW)" $(if $(OUTPUT),--output-dir "$(OUTPUT)")

# Usage: make experiment-c410-plan CORPUS=$HOME/weknora-private-corpus/my_winner_corpus.json [OUTPUT=<private-output-dir>] [ALLOW_MISSING=1]
experiment-c410-plan:
	@test -n "$(CORPUS)" || (echo "Usage: make experiment-c410-plan CORPUS=$$HOME/weknora-private-corpus/my_winner_corpus.json"; exit 2)
	python3 scripts/experiments/build_winner_corpus_matrix.py --corpus "$(CORPUS)" $(if $(OUTPUT),--output-dir "$(OUTPUT)") $(if $(ALLOW_MISSING),--allow-missing-documents)

# Usage: make experiment-c410-inventory DOC_ROOT=/path/to/private/documents [OUTPUT=<private-output-dir>] [EXTS=pdf,docx,md] [MAX_FILES=0]
experiment-c410-inventory:
	@test -n "$(DOC_ROOT)" || (echo "Usage: make experiment-c410-inventory DOC_ROOT=/path/to/private/documents"; exit 2)
	python3 scripts/experiments/inventory_winner_corpus.py --source-dir "$(DOC_ROOT)" $(if $(OUTPUT),--output-dir "$(OUTPUT)") $(if $(EXTS),--extensions "$(EXTS)") $(if $(MAX_FILES),--max-files "$(MAX_FILES)") $(if $(OVERWRITE),--overwrite)

# Usage: make experiment-c410-prepare INVENTORY=$HOME/weknora-private-corpus/inventory/document_inventory.csv OUTPUT=$HOME/weknora-private-corpus/selection [MIN_BYTES=1024] [EXTS=pdf,docx,doc] [OVERWRITE=1]
experiment-c410-prepare:
	@test -n "$(INVENTORY)" || (echo "Usage: make experiment-c410-prepare INVENTORY=/path/to/document_inventory.csv OUTPUT=/private/selection"; exit 2)
	@test -n "$(OUTPUT)" || (echo "Usage: make experiment-c410-prepare INVENTORY=/path/to/document_inventory.csv OUTPUT=/private/selection"; exit 2)
	python3 scripts/experiments/prepare_winner_corpus_selection.py --inventory "$(INVENTORY)" --output-dir "$(OUTPUT)" $(if $(MIN_BYTES),--min-bytes "$(MIN_BYTES)") $(if $(EXTS),--extensions "$(EXTS)") $(if $(OVERWRITE),--overwrite)

# Usage: make experiment-c410-materialize SELECTION=$HOME/weknora-private-corpus/selection/corpus_selection.csv CORPUS=$HOME/weknora-private-corpus/my_winner_corpus.json [NAME=winner_lifecycle_real_pilot] [VARIANT=c2-rules] [OVERWRITE=1]
experiment-c410-materialize:
	@test -n "$(SELECTION)" || (echo "Usage: make experiment-c410-materialize SELECTION=/private/corpus_selection.csv CORPUS=/private/my_winner_corpus.json"; exit 2)
	@test -n "$(CORPUS)" || (echo "Usage: make experiment-c410-materialize SELECTION=/private/corpus_selection.csv CORPUS=/private/my_winner_corpus.json"; exit 2)
	python3 scripts/experiments/materialize_winner_corpus_selection.py --selection "$(SELECTION)" --output "$(CORPUS)" $(if $(NAME),--name "$(NAME)") $(if $(DESCRIPTION),--description "$(DESCRIPTION)") $(if $(VARIANT),--variant "$(VARIANT)") $(if $(MIN_BYTES),--min-bytes "$(MIN_BYTES)") $(if $(ALLOW_MISSING),--allow-missing-documents) $(if $(ALLOW_SOURCE_REUSE),--allow-source-reuse) $(if $(OVERWRITE),--overwrite)

# Usage: make experiment-c410-docreader-fixture
experiment-c410-docreader-fixture:
	python3 scripts/experiments/generate_docreader_fixture.py --verify

# Usage: make experiment-c410-docreader-smoke [OUTPUT=experiments/runs/<run>] [FILE_UPLOAD_TIMEOUT=300]
experiment-c410-docreader-smoke:
	python3 scripts/experiments/run_claims_eval.py --scenario testdata/winner_lifecycle_corpus/docreader_fixture/scenarios/ordered_triplet.json --variant c2-rules $(if $(OUTPUT),--output "$(OUTPUT)") $(if $(FILE_UPLOAD_TIMEOUT),--file-upload-timeout-seconds "$(FILE_UPLOAD_TIMEOUT)")

# Usage: make experiment-c410-docreader-pdf-smoke [OUTPUT=experiments/runs/<run>] [FILE_UPLOAD_TIMEOUT=300]
experiment-c410-docreader-pdf-smoke:
	python3 scripts/experiments/run_claims_eval.py --scenario testdata/winner_lifecycle_corpus/docreader_fixture/scenarios/pdf_claim_smoke.json --variant c2-rules $(if $(OUTPUT),--output "$(OUTPUT)") $(if $(FILE_UPLOAD_TIMEOUT),--file-upload-timeout-seconds "$(FILE_UPLOAD_TIMEOUT)")

# Usage: make experiment-c410-docreader-lifecycle [REPLICATES=1] [OUTPUT=experiments/comparisons/<run>]
experiment-c410-docreader-lifecycle:
	python3 scripts/experiments/run_winner_lifecycle_eval.py --matrix testdata/winner_lifecycle_corpus/docreader_fixture/winner_lifecycle_matrix.json --replicates "$(if $(REPLICATES),$(REPLICATES),1)" $(if $(OUTPUT),--output-dir "$(OUTPUT)")

# Usage: make experiment-c410-synthetic-corpus OUTPUT=$HOME/weknora-private-corpus/synthetic-policy [DEV_FAMILIES=12] [HOLDOUT_FAMILIES=12] [VARIANT=c2-rules] [OVERWRITE=1]
experiment-c410-synthetic-corpus:
	@test -n "$(OUTPUT)" || (echo "Usage: make experiment-c410-synthetic-corpus OUTPUT=$$HOME/weknora-private-corpus/synthetic-policy"; exit 2)
	python3 scripts/experiments/generate_winner_policy_corpus.py --output-dir "$(OUTPUT)" $(if $(DEV_FAMILIES),--development-families "$(DEV_FAMILIES)") $(if $(HOLDOUT_FAMILIES),--holdout-families "$(HOLDOUT_FAMILIES)") $(if $(VARIANT),--variant "$(VARIANT)") $(if $(OVERWRITE),--overwrite)

# Usage: make experiment-c410-fact-eval MATRIX_RUN=experiments/comparisons/<matrix-run> [SPLIT=all|development|holdout] [OUTPUT=<dir>] [ALLOW_FAILED=1] [OVERWRITE=1]
experiment-c410-fact-eval:
	@test -n "$(MATRIX_RUN)" || (echo "Usage: make experiment-c410-fact-eval MATRIX_RUN=experiments/comparisons/<matrix-run>"; exit 2)
	python3 scripts/experiments/score_winner_fact_families.py --matrix-run "$(MATRIX_RUN)" $(if $(SPLIT),--split "$(SPLIT)") $(if $(OUTPUT),--output-dir "$(OUTPUT)") $(if $(ALLOW_FAILED),--allow-failed) $(if $(OVERWRITE),--overwrite)

# Public data adapters keep raw releases and generated documents outside Git.
# WikiFactDiff may stream its public Hugging Face release; set WFD_INPUT for a
# local JSON/JSONL export instead. DEV_PER_LABEL/HOLDOUT_PER_LABEL default in script.
# Usage: make experiment-public-wikifactdiff-plan PUBLIC_OUTPUT=$HOME/weknora-public-data/wikifactdiff-v1 [WFD_INPUT=/data/wfd.jsonl] [DEV_PER_LABEL=10] [HOLDOUT_PER_LABEL=30]
experiment-public-wikifactdiff-plan:
	@test -n "$(PUBLIC_OUTPUT)" || (echo "Usage: make experiment-public-wikifactdiff-plan PUBLIC_OUTPUT=$$HOME/weknora-public-data/wikifactdiff-v1"; exit 2)
	python3 scripts/experiments/prepare_public_wikifactdiff_eval.py --output-dir "$(PUBLIC_OUTPUT)" $(if $(WFD_INPUT),--input "$(WFD_INPUT)") $(if $(WFD_ARCHIVE_MEMBER),--archive-member "$(WFD_ARCHIVE_MEMBER)") $(if $(WFD_HF_DATASET),--hf-dataset "$(WFD_HF_DATASET)") $(if $(WFD_HF_CONFIG),--hf-config "$(WFD_HF_CONFIG)") $(if $(WFD_HF_REVISION),--hf-revision "$(WFD_HF_REVISION)") $(if $(DEV_PER_LABEL),--development-per-label "$(DEV_PER_LABEL)") $(if $(HOLDOUT_PER_LABEL),--holdout-per-label "$(HOLDOUT_PER_LABEL)") $(if $(PUBLIC_VARIANT),--variant "$(PUBLIC_VARIANT)") $(if $(WFD_MAX_SOURCE_RECORDS),--max-source-records "$(WFD_MAX_SOURCE_RECORDS)")

# Usage: make experiment-public-vitaminc-plan VITAMINC_DEVELOPMENT=$HOME/weknora-public-data/vitaminc.zip VITAMINC_HOLDOUT=$HOME/weknora-public-data/vitaminc.zip PUBLIC_OUTPUT=$HOME/weknora-public-data/vitaminc-real-v1 [DEV_PER_LABEL=10] [HOLDOUT_PER_LABEL=30]
experiment-public-vitaminc-plan:
	@test -n "$(VITAMINC_DEVELOPMENT)" || (echo "Usage: make experiment-public-vitaminc-plan VITAMINC_DEVELOPMENT=<real-dev-json-or-zip> VITAMINC_HOLDOUT=<real-test-json-or-zip> PUBLIC_OUTPUT=<dir>"; exit 2)
	@test -n "$(VITAMINC_HOLDOUT)" || (echo "Usage: make experiment-public-vitaminc-plan VITAMINC_DEVELOPMENT=<real-dev-json-or-zip> VITAMINC_HOLDOUT=<real-test-json-or-zip> PUBLIC_OUTPUT=<dir>"; exit 2)
	@test -n "$(PUBLIC_OUTPUT)" || (echo "Usage: make experiment-public-vitaminc-plan ... PUBLIC_OUTPUT=$$HOME/weknora-public-data/vitaminc-real-v1"; exit 2)
	python3 scripts/experiments/prepare_public_vitaminc_eval.py --development-input "$(VITAMINC_DEVELOPMENT)" --holdout-input "$(VITAMINC_HOLDOUT)" --output-dir "$(PUBLIC_OUTPUT)" $(if $(VITAMINC_DEVELOPMENT_MEMBER),--development-member "$(VITAMINC_DEVELOPMENT_MEMBER)") $(if $(VITAMINC_HOLDOUT_MEMBER),--holdout-member "$(VITAMINC_HOLDOUT_MEMBER)") $(if $(DEV_PER_LABEL),--development-per-label "$(DEV_PER_LABEL)") $(if $(HOLDOUT_PER_LABEL),--holdout-per-label "$(HOLDOUT_PER_LABEL)") $(if $(PUBLIC_VARIANT),--variant "$(PUBLIC_VARIANT)")

# Usage: make experiment-public-pair-eval PUBLIC_MANIFEST=$HOME/weknora-public-data/<plan>/pair_eval_manifest.json [SPLIT=development|holdout|all] [PUBLIC_REPLICATES=1] [PUBLIC_MAX_CASES=10] [PUBLIC_TEMPLATE_KB=<id>] [PUBLIC_OUTPUT=<dir>]
experiment-public-pair-eval:
	@test -n "$(PUBLIC_MANIFEST)" || (echo "Usage: make experiment-public-pair-eval PUBLIC_MANIFEST=<pair_eval_manifest.json>"; exit 2)
	python3 scripts/experiments/run_public_pair_eval.py --manifest "$(PUBLIC_MANIFEST)" $(if $(SPLIT),--split "$(SPLIT)") $(if $(PUBLIC_REPLICATES),--replicates "$(PUBLIC_REPLICATES)") $(if $(PUBLIC_MAX_CASES),--max-cases "$(PUBLIC_MAX_CASES)") $(if $(PUBLIC_TEMPLATE_KB),--template-kb-id "$(PUBLIC_TEMPLATE_KB)") $(if $(PUBLIC_CONFLICT_TIMEOUT),--detector-conflict-timeout-seconds "$(PUBLIC_CONFLICT_TIMEOUT)") $(if $(PUBLIC_OUTPUT),--output-dir "$(PUBLIC_OUTPUT)")

# Usage: make experiment-public-pair-dry-run PUBLIC_MANIFEST=$HOME/weknora-public-data/<plan>/pair_eval_manifest.json [SPLIT=development|holdout|all] [PUBLIC_MAX_CASES=10]
experiment-public-pair-dry-run:
	@test -n "$(PUBLIC_MANIFEST)" || (echo "Usage: make experiment-public-pair-dry-run PUBLIC_MANIFEST=<pair_eval_manifest.json>"; exit 2)
	python3 scripts/experiments/run_public_pair_eval.py --manifest "$(PUBLIC_MANIFEST)" $(if $(SPLIT),--split "$(SPLIT)") $(if $(PUBLIC_REPLICATES),--replicates "$(PUBLIC_REPLICATES)") $(if $(PUBLIC_MAX_CASES),--max-cases "$(PUBLIC_MAX_CASES)") $(if $(PUBLIC_CONFLICT_TIMEOUT),--detector-conflict-timeout-seconds "$(PUBLIC_CONFLICT_TIMEOUT)") --dry-run

# Usage: make experiment-public-pair-resummarize PUBLIC_RUN=$HOME/weknora-public-data/<plan>/runs/<run>
experiment-public-pair-resummarize:
	@test -n "$(PUBLIC_RUN)" || (echo "Usage: make experiment-public-pair-resummarize PUBLIC_RUN=<completed-public-pair-run>"; exit 2)
	python3 scripts/experiments/resummarize_public_pair_eval.py --run-dir "$(PUBLIC_RUN)" --apply

experiment-public-self-test:
	python3 scripts/experiments/test_public_benchmark_adapters.py

experiment-c4:
	python3 scripts/experiments/run_claims_eval.py \
		--scenario scripts/experiments/scenarios/c4_cluster_triplet.json --variant c2-batch

experiment-c4-fuzzy:
	python3 scripts/experiments/run_claims_eval.py \
		--scenario scripts/experiments/scenarios/c4_fuzzy_fallback.json --variant c2-batch

# Usage: make experiment-c4-resolve RUN=experiments/runs/<c4-run> [RESOLUTION=resolved_keep_both|resolved_not_conflict] [CLUSTER_ID=<id>]
experiment-c4-resolve:
	@test -n "$(RUN)" || (echo "Usage: make experiment-c4-resolve RUN=experiments/runs/<c4-run>"; exit 2)
	python3 scripts/experiments/run_cluster_resolution.py --run-dir "$(RUN)" \
		--resolution "$(if $(RESOLUTION),$(RESOLUTION),resolved_keep_both)" $(if $(CLUSTER_ID),--cluster-id "$(CLUSTER_ID)")

experiment-p2:
	python3 scripts/experiments/run_claims_eval.py \
		--scenario scripts/experiments/scenarios/p2_claim_chain.json --variant c1

experiment-p3:
	python3 scripts/experiments/run_claims_eval.py \
		--scenario scripts/experiments/scenarios/p3_fallback.json --variant c1

experiment-p12:
	python3 scripts/experiments/run_claims_eval.py \
		--scenario scripts/experiments/scenarios/p1_p2_full_context.json --variant c1

experiment-v1:
	python3 scripts/experiments/run_claims_eval.py \
		--scenario scripts/experiments/scenarios/c1_full.json --variant v1

# Usage: make experiment-audit RUN=experiments/runs/<run-id>
experiment-audit:
	@test -n "$(RUN)" || (echo "Usage: make experiment-audit RUN=experiments/runs/<run-id>"; exit 2)
	python3 scripts/experiments/export_claim_audit.py --run-dir "$(RUN)"

# Usage: make experiment-audit-summary AUDIT=experiments/runs/<run-id>/claim_audit
experiment-audit-summary:
	@test -n "$(AUDIT)" || (echo "Usage: make experiment-audit-summary AUDIT=experiments/runs/<run-id>/claim_audit"; exit 2)
	python3 scripts/experiments/summarize_claim_audit.py --audit-dir "$(AUDIT)"

# Usage: make experiment-audit-metrics AUDIT_CSV=<audit_rows_relabel.csv> SEMANTIC_REVIEW=<prediction_semantic_review.csv>
experiment-audit-metrics:
	@test -n "$(AUDIT_CSV)" || (echo "Usage: make experiment-audit-metrics AUDIT_CSV=<audit_rows_relabel.csv> SEMANTIC_REVIEW=<prediction_semantic_review.csv>"; exit 2)
	@test -n "$(SEMANTIC_REVIEW)" || (echo "Usage: make experiment-audit-metrics AUDIT_CSV=<audit_rows_relabel.csv> SEMANTIC_REVIEW=<prediction_semantic_review.csv>"; exit 2)
	python3 scripts/experiments/compute_reviewed_claim_metrics.py \
		--audit-csv "$(AUDIT_CSV)" --semantic-review "$(SEMANTIC_REVIEW)"

# Usage: make experiment-gold-v2-review ADDITIONS=<reviewed_metrics/gold_v2_additions.csv> REVIEW=<gold_v2_additions_review.csv>
experiment-gold-v2-review:
	@test -n "$(ADDITIONS)" || (echo "Usage: make experiment-gold-v2-review ADDITIONS=<reviewed_metrics/gold_v2_additions.csv> REVIEW=<gold_v2_additions_review.csv>"; exit 2)
	@test -n "$(REVIEW)" || (echo "Usage: make experiment-gold-v2-review ADDITIONS=<reviewed_metrics/gold_v2_additions.csv> REVIEW=<gold_v2_additions_review.csv>"; exit 2)
	python3 scripts/experiments/prepare_gold_v2_review.py \
		--additions "$(ADDITIONS)" --output "$(REVIEW)"

# Usage: make experiment-gold-v2 ADDITIONS=<gold_v2_additions_review.csv> OUTPUT=<candidate-gold-dir>
experiment-gold-v2:
	@test -n "$(ADDITIONS)" || (echo "Usage: make experiment-gold-v2 ADDITIONS=<gold_v2_additions_review.csv> OUTPUT=<candidate-gold-dir>"; exit 2)
	@test -n "$(OUTPUT)" || (echo "Usage: make experiment-gold-v2 ADDITIONS=<gold_v2_additions_review.csv> OUTPUT=<candidate-gold-dir>"; exit 2)
	python3 scripts/experiments/materialize_gold_v2.py \
		--additions "$(ADDITIONS)" --output "$(OUTPUT)"

# Usage: make experiment-gold-v2-scope-review CANDIDATE=<gold-v2-candidate-dir> REVIEW=<scope-review.csv>
experiment-gold-v2-scope-review:
	@test -n "$(CANDIDATE)" || (echo "Usage: make experiment-gold-v2-scope-review CANDIDATE=<gold-v2-candidate-dir> REVIEW=<scope-review.csv>"; exit 2)
	@test -n "$(REVIEW)" || (echo "Usage: make experiment-gold-v2-scope-review CANDIDATE=<gold-v2-candidate-dir> REVIEW=<scope-review.csv>"; exit 2)
	python3 scripts/experiments/prepare_gold_v2_scope_review.py \
		--candidate-dir "$(CANDIDATE)" --output "$(REVIEW)"

# Usage: make experiment-gold-v2-apply-recommendations REVIEW=<scope-review.csv> OUTPUT=<recommended-review.csv>
experiment-gold-v2-apply-recommendations:
	@test -n "$(REVIEW)" || (echo "Usage: make experiment-gold-v2-apply-recommendations REVIEW=<scope-review.csv> OUTPUT=<recommended-review.csv>"; exit 2)
	@test -n "$(OUTPUT)" || (echo "Usage: make experiment-gold-v2-apply-recommendations REVIEW=<scope-review.csv> OUTPUT=<recommended-review.csv>"; exit 2)
	python3 scripts/experiments/apply_gold_v2_scope_recommendations.py \
		--review "$(REVIEW)" --output "$(OUTPUT)"

# Usage: make experiment-gold-v2-finalize CANDIDATE=<full-candidate> SCOPE=<recommended-scope.csv> BROAD_OUTPUT=<dir> NARROW_MANIFEST=<json>
experiment-gold-v2-finalize:
	@test -n "$(CANDIDATE)" || (echo "Usage: make experiment-gold-v2-finalize CANDIDATE=<full-candidate> SCOPE=<recommended-scope.csv> BROAD_OUTPUT=<dir> NARROW_MANIFEST=<json>"; exit 2)
	@test -n "$(SCOPE)" || (echo "Usage: make experiment-gold-v2-finalize CANDIDATE=<full-candidate> SCOPE=<recommended-scope.csv> BROAD_OUTPUT=<dir> NARROW_MANIFEST=<json>"; exit 2)
	@test -n "$(BROAD_OUTPUT)" || (echo "Usage: make experiment-gold-v2-finalize CANDIDATE=<full-candidate> SCOPE=<recommended-scope.csv> BROAD_OUTPUT=<dir> NARROW_MANIFEST=<json>"; exit 2)
	@test -n "$(NARROW_MANIFEST)" || (echo "Usage: make experiment-gold-v2-finalize CANDIDATE=<full-candidate> SCOPE=<recommended-scope.csv> BROAD_OUTPUT=<dir> NARROW_MANIFEST=<json>"; exit 2)
	python3 scripts/experiments/finalize_gold_v2_scopes.py \
		--candidate-dir "$(CANDIDATE)" --scope-review "$(SCOPE)" \
		--broad-output "$(BROAD_OUTPUT)" --narrow-manifest "$(NARROW_MANIFEST)"

# Usage: make experiment-dual-scope-metrics METRICS=<reviewed_metrics.json> MAPPINGS=<accepted_semantic_mappings.csv> SCOPE=<recommended-scope.csv> NARROW_MANIFEST=<json> OUTPUT=<json>
experiment-dual-scope-metrics:
	@test -n "$(METRICS)" || (echo "Usage: make experiment-dual-scope-metrics METRICS=<reviewed_metrics.json> MAPPINGS=<accepted_semantic_mappings.csv> SCOPE=<recommended-scope.csv> NARROW_MANIFEST=<json> OUTPUT=<json>"; exit 2)
	@test -n "$(MAPPINGS)" || (echo "Usage: make experiment-dual-scope-metrics METRICS=<reviewed_metrics.json> MAPPINGS=<accepted_semantic_mappings.csv> SCOPE=<recommended-scope.csv> NARROW_MANIFEST=<json> OUTPUT=<json>"; exit 2)
	@test -n "$(SCOPE)" || (echo "Usage: make experiment-dual-scope-metrics METRICS=<reviewed_metrics.json> MAPPINGS=<accepted_semantic_mappings.csv> SCOPE=<recommended-scope.csv> NARROW_MANIFEST=<json> OUTPUT=<json>"; exit 2)
	@test -n "$(NARROW_MANIFEST)" || (echo "Usage: make experiment-dual-scope-metrics METRICS=<reviewed_metrics.json> MAPPINGS=<accepted_semantic_mappings.csv> SCOPE=<recommended-scope.csv> NARROW_MANIFEST=<json> OUTPUT=<json>"; exit 2)
	@test -n "$(OUTPUT)" || (echo "Usage: make experiment-dual-scope-metrics METRICS=<reviewed_metrics.json> MAPPINGS=<accepted_semantic_mappings.csv> SCOPE=<recommended-scope.csv> NARROW_MANIFEST=<json> OUTPUT=<json>"; exit 2)
	python3 scripts/experiments/compute_dual_scope_metrics.py \
		--reviewed-metrics "$(METRICS)" --mappings "$(MAPPINGS)" \
		--scope-review "$(SCOPE)" --narrow-manifest "$(NARROW_MANIFEST)" --output "$(OUTPUT)"


