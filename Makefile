.PHONY: help build run test clean docker-build-app docker-build-docreader docker-build-frontend docker-build-all docker-run migrate-up migrate-down docker-restart docker-stop start-all stop-all start-ollama stop-ollama build-images build-images-app build-images-docreader build-images-frontend clean-images check-env list-containers pull-images show-platform dev-start dev-stop dev-restart dev-logs dev-status dev-app dev-frontend docs install-swagger build-lite run-lite package-lite experiment-check experiment-c1 experiment-c2-rules experiment-c2-batch experiment-c2-compare experiment-c4 experiment-c4-fuzzy experiment-c4-resolve experiment-p2 experiment-p3 experiment-p12 experiment-v1 experiment-audit experiment-audit-summary experiment-audit-metrics experiment-gold-v2 experiment-gold-v2-review experiment-gold-v2-scope-review experiment-gold-v2-apply-recommendations experiment-gold-v2-finalize experiment-dual-scope-metrics

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


