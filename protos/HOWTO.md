# gRPC-Protobuf文件构建指引

## Go语言

1. 安装[protoc](https://github.com/protocolbuffers/protobuf/releases/download/v21.12/protoc-21.12-linux-x86_64.zip)和[proto-gen-go](https://github.com/protocolbuffers/protobuf-go/releases/download/v1.32.0/protoc-gen-go.v1.32.0.linux.amd64.tar.gz)命令行工具
2. 安装符合你go版本的库，eg,对于go 1.20:
   3. go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.31.0
   4. go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.3.0
3. 假定你的Go项目代码仓库为`github.com/foo/bar`，并且已经初始化（若未初始化，请执行命令`go mod init github.com/foo/bar`命令完成初始化）
4. 将本目录(protos目录）复制到你的Go项目代码仓库中（务必放置在该仓库的根目录中，否则你需要自行修改`protos/build_go.sh`脚本）
5. 在仓库根目录中执行命令：`sh protos/build_go.sh`
6. 命令执行结束后，代码会生成到仓库根目录的`protogen`子目录中
7. 在Go代码中引用相关package，例：`import "github.com/foo/bar/protogen/products/understanding/ast"`
