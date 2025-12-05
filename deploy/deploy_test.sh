#!/bin/bash

# Dinq Message 测试环境部署脚本
# 用法: ./deploy_test.sh [deploy|start|stop|restart|logs|status|clean|shell]

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 日志函数
log_info() {
    echo -e "${GREEN}[TEST-INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[TEST-WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[TEST-ERROR]${NC} $1"
}

log_env() {
    echo -e "${BLUE}[TEST-ENV]${NC} $1"
}

# 检查 .env.test 文件
check_env() {
    if [ ! -f ../.env.test ]; then
        log_error ".env.test 文件不存在"
        log_info "请创建 .env.test 配置文件"
        exit 1
    fi
    log_env "使用配置文件: .env.test"
}

# 检查 Docker 和 Docker Compose
check_docker() {
    if ! command -v docker &> /dev/null; then
        log_error "Docker 未安装"
        exit 1
    fi

    if ! command -v docker-compose &> /dev/null && ! docker compose version &> /dev/null; then
        log_error "Docker Compose 未安装"
        exit 1
    fi
}

# 部署服务（重新构建 + 启动）
deploy() {
    log_info "部署 Dinq Message 测试环境（重新构建）..."
    check_env
    check_docker

    # 构建并启动
    docker-compose -f docker-compose.test.yml up -d --build

    log_info "测试环境服务部署完成"
    log_info "等待服务健康检查..."
    sleep 5
    status
}

# 启动服务（不重新构建）
start() {
    log_info "启动 Dinq Message 测试环境..."
    check_env
    check_docker

    # 启动（不构建）
    docker-compose -f docker-compose.test.yml up -d

    log_info "测试环境服务已启动"
    log_info "等待服务健康检查..."
    sleep 5
    status
}

# 停止服务
stop() {
    log_info "停止 Dinq Message 测试环境..."
    docker-compose -f docker-compose.test.yml down
    log_info "测试环境服务已停止"
}

# 重启服务（不重新构建，只重启容器）
restart() {
    log_info "重启 Dinq Message 测试环境（不重新构建）..."
    docker-compose -f docker-compose.test.yml restart

    log_info "测试环境服务已重启"
    sleep 3
    status
}

# 查看日志
logs() {
    log_info "查看测试环境服务日志 (Ctrl+C 退出)..."
    docker-compose -f docker-compose.test.yml logs -f dinq_message_test
}

# 查看状态
status() {
    log_info "检查测试环境服务状态..."

    # 检查容器状态
    if docker ps | grep -q dinq_message_test; then
        log_info "✓ dinq_message_test 容器运行中"
    else
        log_error "✗ dinq_message_test 容器未运行"
    fi

    # 检查健康状态
    echo ""
    log_info "健康检查 (端口: 9080)..."
    if curl -s http://localhost:9080/health > /dev/null; then
        log_info "✓ API 服务正常"
        curl -s http://localhost:9080/health | python3 -m json.tool
    else
        log_error "✗ API 服务无响应"
    fi
}

# 清理资源
clean() {
    log_warn "这将删除测试环境所有容器和数据卷！"
    read -p "确定要继续吗? (y/N) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        log_info "清理测试环境所有资源..."
        docker-compose -f docker-compose.test.yml down -v
        log_info "测试环境清理完成"
    else
        log_info "取消清理"
    fi
}

# 进入容器 shell
shell() {
    log_info "进入 dinq_message_test 容器..."
    docker-compose -f docker-compose.test.yml exec dinq_message_test sh
}

# 主函数
case "${1:-}" in
    deploy)
        deploy
        ;;
    start)
        start
        ;;
    stop)
        stop
        ;;
    restart)
        restart
        ;;
    logs)
        logs
        ;;
    status)
        status
        ;;
    clean)
        clean
        ;;
    shell)
        shell
        ;;
    *)
        echo "Dinq Message 测试环境部署脚本"
        echo ""
        echo "用法: $0 {deploy|start|stop|restart|logs|status|clean|shell}"
        echo ""
        echo "命令说明:"
        echo "  deploy  - 部署测试服务（重新构建 + 启动）"
        echo "  start   - 启动测试服务（不重新构建）"
        echo "  stop    - 停止测试服务"
        echo "  restart - 重启测试服务（不重新构建）"
        echo "  logs    - 查看日志"
        echo "  status  - 查看状态"
        echo "  clean   - 清理所有容器和数据"
        echo "  shell   - 进入容器 shell"
        echo ""
        echo "测试环境端口: 9080"
        echo "配置文件: .env.test"
        exit 1
        ;;
esac
