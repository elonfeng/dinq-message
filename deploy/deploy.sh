#!/bin/bash

# Dinq Message 部署脚本
# 用法: ./deploy.sh [deploy|start|stop|restart|logs|status|clean|shell]

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 日志函数
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 检查 .env 文件
check_env() {
    if [ ! -f ../.env ]; then
        log_error ".env 文件不存在"
        log_info "请复制 .env.example 为 .env 并填写配置"
        log_info "  cd .. && cp .env.example .env"
        exit 1
    fi
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
    log_info "部署 Dinq Message 服务（重新构建）..."
    check_env
    check_docker

    # 构建并启动
    docker-compose up -d --build

    log_info "服务部署完成"
    log_info "等待服务健康检查..."
    sleep 5
    status
}

# 启动服务（不重新构建）
start() {
    log_info "启动 Dinq Message 服务..."
    check_env
    check_docker

    # 启动（不构建）
    docker-compose up -d

    log_info "服务已启动"
    log_info "等待服务健康检查..."
    sleep 5
    status
}

# 停止服务
stop() {
    log_info "停止 Dinq Message 服务..."
    docker-compose down
    log_info "服务已停止"
}

# 重启服务（不重新构建，只重启容器）
restart() {
    log_info "重启 Dinq Message 服务（不重新构建）..."
    docker-compose restart

    log_info "服务已重启"
    sleep 3
    status
}

# 查看日志
logs() {
    log_info "查看服务日志 (Ctrl+C 退出)..."
    docker-compose logs -f dinq_message
}

# 查看状态
status() {
    log_info "检查服务状态..."

    # 检查容器状态
    if docker ps | grep -q dinq_message; then
        log_info "✓ dinq_message 容器运行中"
    else
        log_error "✗ dinq_message 容器未运行"
    fi

    # 检查健康状态
    echo ""
    log_info "健康检查..."
    if curl -s http://localhost:80/health > /dev/null; then
        log_info "✓ API 服务正常"
        curl -s http://localhost:80/health | python3 -m json.tool
    else
        log_error "✗ API 服务无响应"
    fi
}

# 清理资源
clean() {
    log_warn "这将删除所有容器和数据卷！"
    read -p "确定要继续吗? (y/N) " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        log_info "清理所有资源..."
        docker-compose down -v
        log_info "清理完成"
    else
        log_info "取消清理"
    fi
}

# 进入容器 shell
shell() {
    log_info "进入 dinq_message 容器..."
    docker-compose exec dinq_message sh
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
        echo "用法: $0 {deploy|start|stop|restart|logs|status|clean|shell}"
        echo ""
        echo "命令说明:"
        echo "  deploy  - 部署服务（重新构建 + 启动）"
        echo "  start   - 启动服务（不重新构建）"
        echo "  stop    - 停止服务"
        echo "  restart - 重启服务（不重新构建，避免 build 堆积）"
        echo "  logs    - 查看日志"
        echo "  status  - 查看状态"
        echo "  clean   - 清理所有容器和数据"
        echo "  shell   - 进入容器 shell"
        exit 1
        ;;
esac
