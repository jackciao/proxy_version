#!/bin/bash
set -e

# 配置你的 Docker Hub 用户名和镜像名
# 请修改这里！
DOCKER_USER="your_docker_username"
IMAGE_NAME="proxy_version"
TAG="latest"

FULL_IMAGE_NAME="${DOCKER_USER}/${IMAGE_NAME}:${TAG}"

echo "1. 构建镜像..."
docker build -t "${FULL_IMAGE_NAME}" .

echo "2. 推送镜像 (需要先执行 docker login)..."
echo "正在推送: ${FULL_IMAGE_NAME}"
# 取消下面这行的注释来执行推送
# docker push "${FULL_IMAGE_NAME}"

echo "完成！"
echo "您现在的镜像地址是: ${FULL_IMAGE_NAME}"
echo "请将此地址更新到 deploy/install.sh 脚本中的 IMAGE_NAME 变量。"
