# 部署 magpie 和 magpie-console（Linux 服务器）

本页两部分：magpie 本身的首次安装与日常更新，以及 magpie-console 的取得、安装和更新。

magpie-console 是独立项目，源码在私有仓库
<https://github.com/qiqll/magpie-console>（`main` 分支），有自己的二进制，
只通过 HTTP 调用 magpie 的 `/v1/magpie/admin/*`。**magpie 没有启动
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

## 3. 取得 console 源码

仓库 `https://github.com/qiqll/magpie-console` 是**私有**的。服务器上直接
`git clone https://github.com/qiqll/magpie-console.git` 会要求输入账号和密码；
GitHub 从 2021 年起不再接受账号密码，填登录密码会报
`Password authentication is not supported for Git operations`。Password 一栏
要填 token，或者改用下面任一种方式。三选一：

- A 谁都能用，只要这个 GitHub 账号能看到仓库（owner 或被邀请的协作者）。
- B 和 C 由仓库 owner（qiqll）在 GitHub 上操作：fine-grained token 只能选
  token 主人名下的仓库，deploy key 只有仓库管理员能加。协作者选 A。

### 方式 A：gh 登录（最省事）

```sh
gh auth login            # GitHub.com → HTTPS → 让 gh 配置 git 凭据 → 浏览器或粘贴 token
gh repo clone qiqll/magpie-console
```

`gh auth login` 用的是你自己的账号，权限比只读宽：服务器上通常没有 keyring，
gh 的 token 明文存在 `~/.config/gh/hosts.yml`，能读写你所有的仓库。服务器是
多人共用的就选 B 或 C；clone 完不再需要时 `gh auth logout`（之后 `git pull`
也要重新登录）。
服务器没装 gh 时见 <https://github.com/cli/cli/blob/trunk/docs/install_linux.md>。

### 方式 B：只读 fine-grained token

1. GitHub 网页：**Settings → Developer settings → Personal access tokens →
   Fine-grained tokens → Generate new token**。
2. **Repository access** 选 *Only select repositories*，只勾
   `qiqll/magpie-console`；在 **Permissions** 里加 **Contents: Read-only**
   （Metadata 会自动是只读），其他都不给。设一个过期时间。
3. 服务器上：

   ```sh
   git clone https://github.com/qiqll/magpie-console.git
   # Username: 你的 GitHub 用户名
   # Password: 粘贴上一步的 token（不是登录密码；输入时不回显）
   ```

以后每次 `git pull` 都会再问。想让它记住，clone 时就带上 `store`：

```sh
git clone -c credential.helper=store https://github.com/qiqll/magpie-console.git
```

（已经 clone 过的，`git -C magpie-console config credential.helper store`，
下一次 `git pull` 再输一次 token 后就记住了。）

`store` 把 token **明文**写进 `~/.git-credentials`（权限 600，本用户和 root
可读）。只给了这一个仓库的只读权限，泄露了也只能读这份源码；到期或不用了在
GitHub 上 revoke，并删掉 `~/.git-credentials` 里那一行。不想落盘就用
`git -C magpie-console config credential.helper 'cache --timeout=3600'`
（只在内存里存一小时）。

### 方式 C：只读 deploy key（SSH）

服务器上生成一把只给这个仓库用的钥匙：

```sh
mkdir -p -m 700 ~/.ssh
ssh-keygen -t ed25519 -f ~/.ssh/magpie-console-deploy -N "" -C "magpie-console deploy"
cat ~/.ssh/magpie-console-deploy.pub
```

把打印出的公钥加到 GitHub 仓库 **Settings → Deploy keys → Add deploy key**，
**不要**勾 *Allow write access*。然后在 `~/.ssh/config` 里给它一个别名：

```
Host github-magpie-console
    HostName github.com
    User git
    IdentityFile ~/.ssh/magpie-console-deploy
    IdentitiesOnly yes
```

`~/.ssh/config` 不能让别人可写，否则 ssh 拒绝读它：`chmod 600 ~/.ssh/config`。
用别名 clone：

```sh
ssh -T github-magpie-console      # 首次会问是否信任 github.com 的 host key，见下
git clone github-magpie-console:qiqll/magpie-console.git
```

`ssh -T` 首次打印的 ED25519 指纹应是
`SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU`（以 GitHub 的
[SSH key fingerprints](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/githubs-ssh-key-fingerprints)
页面为准），对上了再输 `yes`；之后看到 "successfully authenticated" 即可。

deploy key 只对这一个仓库有效、只读，`git pull` 不再问密码。

已经用 HTTPS clone 过、想换成 deploy key：

```sh
git -C magpie-console remote set-url origin github-magpie-console:qiqll/magpie-console.git
```

## 4. 编译并安装 console

需要 Go 1.26。在服务器上编译（服务器不装 Go 时在 Mac 上交叉编译，见本节后面的
「不在服务器上编译」）：

```sh
cd magpie-console
CGO_ENABLED=0 go build -o magpie-console .
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

### 不在服务器上编译：Mac 上交叉编译后 scp

服务器没装 Go，或者不想把源码放到服务器上时，在 Mac 的 console 目录里编译
Linux 版本再拷过去。console 用纯 Go 的 sqlite，`CGO_ENABLED=0` 编出来是静态
二进制，不依赖服务器上的库。先在服务器上 `uname -m` 看架构：`x86_64` 用
`amd64`，`aarch64` 用 `arm64`。

```sh
cd <Mac 上的 magpie-console 目录>
git pull
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o magpie-console-linux .
#   ARM 服务器把 GOARCH=amd64 换成 GOARCH=arm64，其余不变
scp magpie-console-linux op@<服务器>:~/magpie-console.new
```

拷到自己家目录而不是 `/tmp`：`/tmp` 人人可写，多人服务器上别人可以抢先建好
同名文件，在 `sudo install` 之前换掉内容。服务器上：

```sh
sudo install -m 0755 ~/magpie-console.new /usr/local/bin/magpie-console      # 首次安装
sudo install -m 0755 ~/magpie-console.new "$(command -v magpie-console)"     # 更新：和服务的 ExecStart 同一路径
rm ~/magpie-console.new
```

首次安装接着跑上面的 `init`、`set-key`、`install-service`；更新时接着
`systemctl --user restart magpie-console`。

### 更新 console

和更新 magpie 一样，在服务器的 console 源码目录里：

```sh
cd <console 源码目录>
git checkout main                          # 回退过的话先回到分支上
git pull --ff-only                         # 只快进；方式 B 没存凭据时会再问，Password 栏粘贴 token
git log --oneline -1                       # 确认是刚推送的那个提交
CGO_ENABLED=0 go build -o magpie-console .
sudo install -m 0755 magpie-console "$(command -v magpie-console)"   # 和服务的 ExecStart 同一路径
systemctl --user restart magpie-console
journalctl --user -u magpie-console -n 30 --no-pager
```

- `"$(command -v magpie-console)"` 要和 `systemctl --user cat magpie-console`
  里 `ExecStart` 的路径一致，否则重启后还是旧版本。
- 在 Mac 上编译的，按上一节交叉编译、`scp`、`install`，再 `restart`。
- 回退：`git checkout <上一个提交>`，再编译、`install`、`restart`；下次更新
  先 `git checkout main`（上面第一行）。数据目录不随版本变，更新前可以先 `magpie-console backup` 一份（见第 6 节）。

## 5. 安全

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

## 6. 备份与恢复

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
