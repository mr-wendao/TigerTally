# 部署说明

本目录记录**服务器侧怎么推代码**。

## 为什么用部署密钥，而不是账号 Token

服务器有公网 IP。把 GitHub 账号的 token 放在那里，一旦泄露，
损失的是**整个账号** —— 能建仓库、能删仓库、能改别的项目。

部署密钥（Deploy Key）不一样：

| | 账号 Token | **部署密钥** |
|---|---|---|
| 作用范围 | 整个账号 | **仅此一个仓库** |
| 能删仓库吗 | 能 | 不能 |
| 能碰别的项目吗 | 能 | 不能 |
| 能改账号设置吗 | 能 | 不能 |
| 撤销 | 能 | 能，一键 |

**够用就好，多一分权限都不要。**

## 已经配好的东西

服务器侧仓库在 `~/tigertally`，SSH 别名 `github-tigertally`：

```
Host github-tigertally
  HostName github.com
  User git
  IdentityFile ~/.ssh/tigertally_deploy
  IdentitiesOnly yes
```

新机器从零配置：`bash deploy/setup-server.sh`

## 推送身份

```
xiaoma-bot <xiaoma@minis.local>
```

只配在仓库局部（`git config` 不带 `--global`），**不污染服务器的全局配置**。

## 注意

- 部署密钥**绑的是仓库本身，不是仓库名字** —— 仓库改名不影响，无需重新添加
- 私钥 `~/.ssh/tigertally_deploy` 权限 600，**永不进仓库**（`.gitignore` 已挡）
- 撤销：GitHub 仓库 → Settings → Deploy keys
