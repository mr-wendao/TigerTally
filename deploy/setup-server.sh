#!/bin/bash
# 在服务器上配置 BaiYing 仓库的推送环境
set -e

mkdir -p ~/.ssh && chmod 700 ~/.ssh

# 1. SSH 别名：让 github-baiying 走这把部署密钥
printf 'Host github-baiying\n  HostName github.com\n  User git\n  IdentityFile ~/.ssh/baiying_deploy\n  IdentitiesOnly yes\n' > ~/.ssh/config
chmod 600 ~/.ssh/config

# 2. known_hosts
touch ~/.ssh/known_hosts && chmod 600 ~/.ssh/known_hosts
if ! grep -q "github.com" ~/.ssh/known_hosts 2>/dev/null; then
  ssh-keyscan -t ed25519 github.com 2>/dev/null >> ~/.ssh/known_hosts
  sort -u ~/.ssh/known_hosts -o ~/.ssh/known_hosts
fi

echo "--- 1. 主机指纹（应为 SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU）---"
ssh-keygen -lf ~/.ssh/known_hosts 2>/dev/null | grep -i github || echo "  (无)"

echo "--- 2. 密钥认证测试 ---"
ssh -o StrictHostKeyChecking=yes -T git@github-baiying 2>&1 | head -2 || true

echo "--- 3. 克隆仓库 ---"
cd ~
if [ -d ~/baiying/.git ]; then
  echo "  已存在，拉取最新"
  cd ~/baiying && git pull --ff-only 2>&1 | tail -2
else
  git clone git@github-baiying:mr-wendao/BaiYing.git ~/baiying 2>&1 | tail -3
  cd ~/baiying
fi

# 4. 提交身份（用仓库局部配置，不污染全局）
git config user.name "xiaoma-bot"
git config user.email "xiaoma@minis.local"
echo "--- 4. 仓库状态 ---"
git remote -v
git log --oneline | head -3
git config user.name
