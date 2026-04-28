#!/bin/bash

# TranslatorAudio 完整卸载脚本

set -e

echo "正在卸载 TranslatorAudio..."
echo ""

# 删除应用程序
if [ -d "/Applications/TranslatorAudio.app" ]; then
    # 检查应用是否在运行
    if pgrep -f "TranslatorAudio.app" > /dev/null; then
        echo "关闭正在运行的应用..."
        pkill -f "TranslatorAudio.app" 2>/dev/null || true
        sleep 1
    fi
    
    sudo rm -rf /Applications/TranslatorAudio.app
    echo "✅ 应用程序 已删除"
else
    echo "⚠️  应用程序未安装"
fi

# 删除Driver
if [ -d "/Library/Audio/Plug-Ins/HAL/translatorAudio.driver" ]; then
    sudo rm -rf /Library/Audio/Plug-Ins/HAL/translatorAudio.driver
    echo "✅ Driver 已删除"
else
    echo "⚠️  Driver 未安装"
fi

# 重启coreaudiod
echo ""
echo "重启 coreaudiod 服务..."
sudo killall coreaudiod 2>/dev/null || true

echo ""
echo "✅ 卸载完成"
echo ""
echo "注意: 配置文件在应用包内，已随应用一起删除"
