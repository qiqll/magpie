# 部署 magpie 和 magpie-console（Linux 服务器）

本页两部分：magpie 本身的首次安装与日常更新，以及 magpie-console 的安装。

magpie-console 是独立项目（与本仓库并列的 `../magpie-console`），有自己的
二进制，只通过 HTTP 调用 magpie 的 `/v1/magpie/admin/*`。**magpie 没有启动
console 的参数**：`magpie daemon install --web` 只装 magpie 自己，console 要
单独装成一个 systemd 用户服务。

更完整的说明在 console 仓库：`README.md`（运行、备份、部署要点）和
`docs/operator.md`（超级管理员从零开始）。区域与密钥在 magpie 一侧的实现见
[subsystems/areas.md](subsystems/areas.md)；`magpie daemon` 的全部参数见
[reference.md](reference.md)。

推荐布局：两者在同一台 Linux 机器上，console 经 loopback 访问 magpie。

| | 监听 | 说明 |
| --- | --- | --- |
| magpie 网关 | `127.0.0.1:3425` | console 经这里调用 admin 接口 |
| magpie 页面 | `127.0.0.1:3430` | 只在本机，经 SSH 隧道打开 |
| magpie-console | `0.0.0.0:7790`（可改） | 明文 HTTP，给区域管理员用 |

## 1. magpie 作为服务运行

```sh
sudo install magpie /usr/local/bin/
sudo loginctl enable-linger "$USER"      # 开机即起，没人登录也运行
magpie daemon install --web              # 网关 127.0.0.1:3425，页面 127.0.0.1:3430
magpie daemon status                     # 打印页面链接 http://127.0.0.1:3430/?k=…
```

在自己电脑上开隧道，再打开 `daemon status` 打印的链接：

```sh
ssh -L 3430:127.0.0.1:3430 op@<服务器>
```

### 更新 magpie（服务器上从源码）

服务器上有一份本仓库的 checkout，`origin` 指向推送 `flyingfish` 分支的远程。
本机测试通过、推送到远程后，在服务器上：

```sh
cd <magpie 源码目录>
git pull
git merge --ff-only origin/flyingfish      # 只快进；本地有分叉时会拒绝，不会产生合并提交
git log --oneline -1                       # 确认是刚推送的那个提交
make cli                                   # CGO_ENABLED=0、-tags nogui，生成 ./magpie
sudo install -m 0755 ./magpie "$(command -v magpie)"   # 覆盖服务正在用的那个二进制
systemctl --user restart magpie
journalctl --user -u magpie -n 30 --no-pager           # 看启动日志有没有报错
```

- `make cli` 是不带窗口的版本（`nogui`），服务器用它就够了；需要 Go 1.26。
- `"$(command -v magpie)"` 要和服务的 `ExecStart` 是同一个路径
  （`systemctl --user cat magpie` 可以看到），否则重启后还是旧版本。
- 重启会断开正在进行的请求；客户端会重试，但最好挑空闲时间。
- 回退：`git checkout <上一个提交>`，再 `make cli`、`install`、`restart`。

## 2. 在 magpie 页面里准备

1. **供应商**：加好要开放给区域的 API key 提供方（console 的"可添加的提供方"
   只列出 magpie 里已有的）。订阅类不需要这一步。
2. **网关 → 区域 → 控制台密钥 → 生成**。页面不显示密钥，只复制到你电脑的
   剪贴板。
3. **管理员允许列表**留空即可：同机部署时本机总是允许。

## 3. 编译并安装 console

需要 Go 1.26。在服务器上编译，或在 Mac 上交叉编译后拷过去：

```sh
cd magpie-console
GOOS=linux GOARCH=amd64 go build -o magpie-console .   # 服务器上编译时去掉 GOOS/GOARCH
sudo install magpie-console /usr/local/bin/            # 服务记的是二进制的绝对路径

magpie-console init                                    # 上级用户名和密码（至少 10 位）
MAGPIE_URL=http://127.0.0.1:3425 magpie-console set-key
#   粘贴第 2 步的控制台密钥；显示 "Kept." 表示 magpie 确认了它是 console key
magpie-console install-service --addr 0.0.0.0:7790 --magpie http://127.0.0.1:3425
systemctl --user is-active magpie-console              # active
journalctl --user -u magpie-console -f                 # 日志
```

`install-service` 写 `~/.config/systemd/user/magpie-console.service` 并
enable、restart。改设置就带新参数再跑一次；`magpie-console uninstall-service`
停止并删除服务，数据保留。

不用 systemd 时前台运行：

```sh
MAGPIE_URL=http://127.0.0.1:3425 MAGPIE_CONSOLE_ADDR=0.0.0.0:7790 magpie-console serve
```

浏览器打开 `http://<服务器 IP>:7790/`，用 `init` 建的账号登录，在**用户 → 新建
用户**里给每个区域建下级。

### 更新 console

```sh
cd magpie-console && git pull            # 或把新源码拷过去
go build -o magpie-console .
sudo install -m 0755 magpie-console "$(command -v magpie-console)"
systemctl --user restart magpie-console
journalctl --user -u magpie-console -n 30 --no-pager
```

## 4. 安全

- console 对外是**明文 HTTP**，密码和 cookie 可被同网段抓包。用防火墙只放行
  内网网段：

  ```sh
  sudo ufw status                      # inactive 时规则不生效
  sudo ufw allow 22/tcp                # 先放行 SSH
  sudo ufw allow from 192.168.1.0/24 to any port 7790 proto tcp
  sudo ufw default deny incoming
  sudo ufw enable
  ```

  或只监听内网网卡：`install-service --addr <内网 IP>:7790`。
- 跨不可信网络访问时放在 HTTPS 反向代理后，设 `MAGPIE_CONSOLE_SECURE=1` 和
  `MAGPIE_CONSOLE_PROXY=1`，再 `install-service --addr 127.0.0.1:7790`。
- console 不在 magpie 本机时，magpie 要开"共享到局域网"，并把 console 机器的
  IP 加入管理员允许列表；此时 console key 在网络上明文传输。

## 5. 备份与恢复

数据在 `~/.magpie-console`（或 `MAGPIE_CONSOLE_DATA`）：`console.db` 和
`master.key`，**必须一起备份**。

```sh
magpie-console backup /backup/console-2026-11-19      # 运行中也可以，目标须是新目录

systemctl --user stop magpie-console
mv ~/.magpie-console ~/.magpie-console.old            # restore 不覆盖已有数据
magpie-console restore /backup/console-2026-11-19
systemctl --user start magpie-console
```

设了 `MAGPIE_CONSOLE_SECRET` 时主密钥不在文件里，要另行保管。
