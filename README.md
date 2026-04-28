# xingyi_client

本项目现已精简为 **本机直连版语音翻译客户端**：

- 不做版本检查
- 不连接自建登录/计费后端
- 不保存登录态
- 直接连接官方上游：
  - **模型一 / 豆包**：`wss://openspeech.bytedance.com/v4/ast/v2/translate`
  - **模型二 / Qwen**：`wss://dashscope.aliyuncs.com/api-ws/v1/realtime?model=qwen3-livetranslate-flash-realtime`

密钥在 GUI 内直接填写，仅保存在当前进程内存中。

## 运行

### GUI

```bash
go run . --target=gui
```

GUI 中：

- 选择翻译模式
- 选择模型
- 按当前模型填写对应密钥
  - 模型一：豆包 `APP ID` 和 `Access Token`
  - 模型二：DashScope `API Key`

### CLI

CLI 仍可用环境变量预填充密钥：

```bash
export DOUBAO_APP_ID=your_app_id
export DOUBAO_ACCESS_KEY=your_access_token
export QWEN_API_KEY=your_dashscope_api_key
```

然后执行：

```bash
go run . --target=ast
go run . --target=sts
go run . --target=mic2vmic
go run . --target=vspeaker2pspeaker
go run . --target=bidirectional
```

## 测试

```bash
go test ./...
go test -tags=integration ./...
RUN_RACE=1 bash scripts/run-automated-tests.sh
```

## Proto 生成

如需重新生成 proto 代码，参考：

```bash
protos/HOWTO.md
```
