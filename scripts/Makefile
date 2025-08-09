# Oneinstack Panel 脚本管理 Makefile

.PHONY: help generate test test-all lint clean validate install-tools

# 默认目标
help: ## 显示帮助信息
	@echo "Oneinstack Panel 脚本管理工具"
	@echo ""
	@echo "可用命令:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "示例:"
	@echo "  make generate NAME=postgresql TYPE=install"
	@echo "  make test SCRIPT=nginx"
	@echo "  make test-all"
	@echo "  make lint SCRIPT=mysql55"

generate: ## 生成新脚本 (需要 NAME 和 TYPE 参数)
	@if [ -z "$(NAME)" ]; then \
		echo "❌ 错误: 请指定脚本名称"; \
		echo "用法: make generate NAME=postgresql TYPE=install"; \
		exit 1; \
	fi
	@if [ -z "$(TYPE)" ]; then \
		echo "❌ 错误: 请指定脚本类型"; \
		echo "用法: make generate NAME=postgresql TYPE=install"; \
		exit 1; \
	fi
	@echo "🚀 生成脚本: $(NAME) ($(TYPE))"
	@cd .. && go run cmd/script-generator/main.go --name=$(NAME) --type=$(TYPE) $(if $(AUTHOR),--author="$(AUTHOR)") $(if $(EMAIL),--email="$(EMAIL)") $(if $(VERSION),--version="$(VERSION)") $(if $(PORT),--port="$(PORT)") $(if $(DESC),--desc="$(DESC)") $(if $(CATEGORY),--category="$(CATEGORY)") $(if $(INTERACTIVE),-i)

generate-interactive: ## 交互式生成脚本
	@echo "🎯 交互式脚本生成"
	@cd .. && go run cmd/script-generator/main.go -i --name="" --type=install

test: ## 测试指定脚本 (需要 SCRIPT 参数)
	@if [ -z "$(SCRIPT)" ]; then \
		echo "❌ 错误: 请指定要测试的脚本"; \
		echo "用法: make test SCRIPT=nginx"; \
		exit 1; \
	fi
	@echo "🧪 测试脚本: $(SCRIPT)"
	@cd .. && go run cmd/script-tester/main.go --script=$(SCRIPT) $(if $(TYPE),--type=$(TYPE)) $(if $(DRY_RUN),--dry-run) $(if $(VERBOSE),-v) $(if $(OS),--os="$(OS)")

test-all: ## 测试所有脚本
	@echo "🧪 测试所有安装脚本"
	@for script in install/*.sh; do \
		if [ -f "$$script" ]; then \
			name=$$(basename "$$script" .sh); \
			echo ""; \
			echo "📝 测试脚本: $$name"; \
			echo "----------------------------------------"; \
			cd .. && go run cmd/script-tester/main.go --script=$$name --type=install --dry-run || true; \
		fi \
	done



lint: ## 检查脚本代码质量 (需要 SCRIPT 参数)
	@if [ -z "$(SCRIPT)" ]; then \
		echo "❌ 错误: 请指定要检查的脚本"; \
		echo "用法: make lint SCRIPT=nginx"; \
		exit 1; \
	fi
	@echo "🔍 检查脚本: $(SCRIPT)"
	@script_path="$(TYPE)/$(SCRIPT).sh"; \
	if [ ! -f "$$script_path" ]; then \
		script_path="install/$(SCRIPT).sh"; \
	fi; \
	if [ -f "$$script_path" ]; then \
		echo "📝 语法检查..."; \
		bash -n "$$script_path" && echo "✅ 语法正确" || echo "❌ 语法错误"; \
		if command -v shellcheck >/dev/null 2>&1; then \
			echo "🔧 Shellcheck 检查..."; \
			shellcheck -x "$$script_path" || echo "⚠️  发现代码质量问题"; \
		else \
			echo "⚠️  Shellcheck 未安装，跳过代码质量检查"; \
		fi; \
	else \
		echo "❌ 脚本文件不存在: $$script_path"; \
	fi

lint-all: ## 检查所有脚本的代码质量
	@echo "🔍 检查所有脚本"
	@for dir in install uninstall config; do \
		if [ -d "$$dir" ]; then \
			echo ""; \
			echo "📁 检查 $$dir 目录"; \
			echo "----------------------------------------"; \
			for script in $$dir/*.sh; do \
				if [ -f "$$script" ]; then \
					echo "🔍 $$script"; \
					bash -n "$$script" && echo "  ✅ 语法正确" || echo "  ❌ 语法错误"; \
					if command -v shellcheck >/dev/null 2>&1; then \
						shellcheck -x "$$script" >/dev/null 2>&1 && echo "  ✅ 代码质量良好" || echo "  ⚠️  代码质量问题"; \
					fi; \
				fi \
			done \
		fi \
	done

validate-config: ## 验证配置文件
	@echo "⚙️  验证配置文件"
	@if [ -f "config.yaml" ]; then \
		if command -v yq >/dev/null 2>&1; then \
			echo "🔍 YAML 格式检查..."; \
			yq eval '.' config.yaml >/dev/null && echo "✅ 配置文件格式正确" || echo "❌ 配置文件格式错误"; \
		else \
			echo "⚠️  yq 未安装，跳过 YAML 格式检查"; \
		fi; \
		echo "📊 配置统计:"; \
		echo "  安装脚本: $$(find install -name "*.sh" | wc -l)"; \
		echo "  卸载脚本: $$(find uninstall -name "*.sh" 2>/dev/null | wc -l)"; \
		echo "  配置脚本: $$(find config -name "*.sh" 2>/dev/null | wc -l)"; \
	else \
		echo "❌ 配置文件 config.yaml 不存在"; \
	fi

list-scripts: ## 列出所有脚本
	@echo "📋 脚本列表"
	@echo ""
	@for dir in install uninstall config; do \
		if [ -d "$$dir" ]; then \
			echo "📁 $$dir/"; \
			for script in $$dir/*.sh; do \
				if [ -f "$$script" ]; then \
					name=$$(basename "$$script" .sh); \
					desc=$$(head -10 "$$script" | grep "# 描述:" | sed 's/# 描述: //g' || echo "无描述"); \
					printf "  %-20s %s\n" "$$name" "$$desc"; \
				fi \
			done; \
			echo ""; \
		fi \
	done

stats: ## 显示脚本统计信息
	@echo "📊 脚本统计"
	@echo ""
	@total_scripts=0; \
	total_lines=0; \
	for dir in install uninstall config; do \
		if [ -d "$$dir" ]; then \
			count=$$(find $$dir -name "*.sh" | wc -l); \
			lines=$$(find $$dir -name "*.sh" -exec wc -l {} \; | awk '{sum += $$1} END {print sum}'); \
			echo "📁 $$dir: $$count 个脚本, $$lines 行代码"; \
			total_scripts=$$((total_scripts + count)); \
			total_lines=$$((total_lines + lines)); \
		fi \
	done; \
	echo ""; \
	echo "📈 总计: $$total_scripts 个脚本, $$total_lines 行代码"

clean: ## 清理临时文件
	@echo "🧹 清理临时文件"
	@find . -name "*.tmp" -delete
	@find . -name "*.log" -delete
	@find . -name ".DS_Store" -delete
	@find . -name "config-*.yaml" -delete
	@echo "✅ 清理完成"

install-tools: ## 安装开发工具
	@echo "🔧 安装开发工具"
	@echo "检查 shellcheck..."
	@if ! command -v shellcheck >/dev/null 2>&1; then \
		echo "安装 shellcheck..."; \
		if command -v apt-get >/dev/null 2>&1; then \
			sudo apt-get update && sudo apt-get install -y shellcheck; \
		elif command -v yum >/dev/null 2>&1; then \
			sudo yum install -y epel-release && sudo yum install -y ShellCheck; \
		elif command -v brew >/dev/null 2>&1; then \
			brew install shellcheck; \
		else \
			echo "⚠️  请手动安装 shellcheck"; \
		fi; \
	else \
		echo "✅ shellcheck 已安装"; \
	fi
	@echo "检查 yq..."
	@if ! command -v yq >/dev/null 2>&1; then \
		echo "安装 yq..."; \
		sudo wget -qO /usr/local/bin/yq https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64; \
		sudo chmod +x /usr/local/bin/yq; \
	else \
		echo "✅ yq 已安装"; \
	fi
	@echo "✅ 开发工具安装完成"

check-env: ## 检查开发环境
	@echo "🔍 检查开发环境"
	@echo ""
	@echo "Go 版本:"
	@go version 2>/dev/null || echo "❌ Go 未安装"
	@echo ""

	@echo ""
	@echo "Shellcheck:"
	@shellcheck --version 2>/dev/null || echo "❌ Shellcheck 未安装"
	@echo ""
	@echo "yq:"
	@yq --version 2>/dev/null || echo "❌ yq 未安装"

# 快捷命令别名
new: generate ## 生成新脚本的别名
check: lint ## 检查脚本的别名
run-test: test ## 运行测试的别名
