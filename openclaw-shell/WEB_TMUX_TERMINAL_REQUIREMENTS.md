# ClawManager Web Shell tmux 适配要求

OpenClaw Shell Runtime 默认进入同一个 `tmux` 会话。ClawManager Web 端需要把 Shell 视为完整 PTY 终端，而不是普通文本输出窗口。

## 终端渲染

- 前端使用 `xterm.js` 渲染终端输出，并接入 `FitAddon` 做尺寸适配。
- 输出流必须按原始终端字节处理，完整支持 ANSI/CSI/OSC 控制序列、256 色、truecolor、光标移动、清屏、alternate screen、tmux status line、bracketed paste。
- 不得把 ESC 控制字符当普通文本展示；页面中不应出现 `B(B`、`[?2004h`、`[openclaw-0:tmux*]` 这类被错误解析后的残片。
- Web 端需要支持 UTF-8 和 CJK 宽字符，避免中文、emoji、全角符号导致列宽错位。

## WebSocket 协议

- 服务端为每个 Shell 连接分配真实 PTY，并在 PTY 中 attach 到容器内已有 tmux session。
- PTY 输出通过 WebSocket 原样传给前端，推荐 binary frame；如果使用 text frame，也不能按行切分、trim、HTML escape 后再写入终端。
- 前端输入按终端原始输入发送给 PTY，包括普通字符、方向键、Tab、Enter、Backspace、Ctrl-C、Ctrl-D、Ctrl-L、复制粘贴内容。
- 前端只负责终端渲染，不应自行拼接 prompt、状态栏或历史文本。

## 尺寸同步

- 连接建立后，前端立即根据容器尺寸计算 `cols` 和 `rows`，发送 resize 消息给后端。
- 浏览器窗口、面板、字体大小变化时，前端重新发送 resize。
- 后端收到 resize 后调用 PTY resize，使 tmux 重新绘制当前 pane。
- resize 后不能出现状态栏断裂、光标位置错误、长行折行残留。

## 重连与历史

- 断开重连时，后端重新 attach 到同一个 tmux session，而不是启动新的 shell。
- tmux attach 后会重绘当前屏幕，前端应清空旧 xterm buffer 后接收新的 PTY 输出。
- 如需恢复 scrollback，后端可以在 attach 前额外读取 `tmux capture-pane -e -p -S -100000 -t openclaw-shell:0.0`，其中 `-e` 用于保留颜色控制序列。
- Bash 命令历史由镜像持久化到 `/config/openclaw-shell-agent/history/<user>.bash_history`；屏幕输出日志由 `tmux pipe-pane` 写入 `/config/openclaw-shell-agent/logs/openclaw-shell.typescript`。

## 安全处理

- 终端输出只能写入 `xterm.write()`，不能作为 HTML 插入 DOM。
- OSC 52 剪贴板控制序列需要按产品安全策略显式允许或拦截。
- WebSocket 连接关闭时，释放 PTY client，但不要 kill tmux session。

## 验收项

- `printf '\033[31mred\033[0m\n'` 显示红色文本，不出现转义字符文本。
- `ls --color=always` 能显示颜色。
- `vim`、`less`、`top` 这类全屏程序显示正常，退出后屏幕恢复正常。
- tmux status line 位置固定，resize 后不漂移、不残留。
- 断开 WebSocket 后重连，仍回到同一个 tmux session，并能看到断开前的当前屏幕。
- 执行过的 Bash 命令在新连接里可通过方向键或 `history` 找回。
- 大量输出、中文路径、长命令、复制粘贴、多次 resize 都不破坏显示。
