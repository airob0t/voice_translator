#!/bin/bash

# You must have `protoc` CMD tool installed. You can download compiled binary for Linux here:
# https://github.com/protocolbuffers/protobuf/releases/download/v21.12/protoc-21.12-linux-x86_64.zip

# You must also have `proto-gen-go` installed, which you can find the compiled binary here:
# https://github.com/protocolbuffers/protobuf-go/releases/download/v1.32.0/protoc-gen-go.v1.32.0.linux.amd64.tar.gz

set -ex

proto_dir=$(dirname $0)
cd ${proto_dir}/..

import_prefix=$(go list -m)/protogen

protoc \
  --proto_path=${proto_dir} \
  --go_out=. \
  --go_opt=Mcommon/events.proto=${import_prefix}/common/event \
  --go_opt=Mcommon/rpcmeta.proto=${import_prefix}/common/rpcmeta \
  --go_opt=Mproducts/understanding/base/au_base.proto=${import_prefix}/products/understanding/base \
  --go_opt=Mproducts/understanding/ast/ast_service.proto=${import_prefix}/products/understanding/ast \
  --go-grpc_out=. \
  --go-grpc_opt=Mcommon/events.proto=${import_prefix}/common/event \
  --go-grpc_opt=Mcommon/rpcmeta.proto=${import_prefix}/common/rpcmeta \
  --go-grpc_opt=Mproducts/understanding/base/au_base.proto=${import_prefix}/products/understanding/base \
  --go-grpc_opt=Mproducts/understanding/ast/ast_service.proto=${import_prefix}/products/understanding/ast \
  ${proto_dir}/common/events.proto \
  ${proto_dir}/common/rpcmeta.proto \
  ${proto_dir}/products/understanding/base/au_base.proto \
  ${proto_dir}/products/understanding/ast/ast_service.proto


# Copy generated files to the right place and remove redundant directories.
dest_dir=$(basename ${import_prefix})
redundant_dir=${import_prefix%%/*}
rsync -a --remove-source-files ${import_prefix}/ ${dest_dir}/
rm -rf ${redundant_dir}
