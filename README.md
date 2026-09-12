# WeKnora 芯元无界模型插件

这是 `model_provider` 类型的参考插件，通过芯元无界（TokenLake）的 OpenAI 兼容接口提供纯文本对话模型。

插件使用 API Key 动态读取当前账号可用的模型，只向 WeKnora 返回 `modality=chat` 的模型。它只声明 `chat` 能力和 `conversation` 数据权限，网络出口仅允许 `tokenlake.com.cn`。API Key 由 WeKnora 模型配置保存并加密，不写入插件安装记录。

```bash
go test ./...
docker buildx build --load --build-context weknora=../WeKnora \
  -t ghcr.io/zealxun/weknora-tokenlake-model-plugin:0.1.0 .
```

芯元无界 API 文档：<https://tokenlake.com.cn/docs/guide/quickstart>
