# gocan

> Go CAN / CAN FD 多后端库：Windows 支持 PEAK-System PCANBasic、CANable 2.0
> SLCAN-FD 与 MiniCANFD，Linux 支持 SocketCAN、SLCAN 和 MiniCANFD。

[![Go Reference](https://pkg.go.dev/badge/github.com/zhuzx17/gocan.svg)](https://pkg.go.dev/github.com/zhuzx17/gocan)
[![CI](https://github.com/zhuzx17/gocan/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/zhuzx17/gocan/actions/workflows/ci.yml)
[![CodeQL](https://github.com/zhuzx17/gocan/actions/workflows/codeql.yml/badge.svg?branch=main)](https://github.com/zhuzx17/gocan/actions/workflows/codeql.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/zhuzx17/gocan)](https://goreportcard.com/report/github.com/zhuzx17/gocan)
[![codecov](https://codecov.io/gh/zhuzx17/gocan/branch/main/graph/badge.svg)](https://codecov.io/gh/zhuzx17/gocan)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

`gocan` 提供统一的 Go `Bus` API，让程序能在 Windows PCANBasic、CANable 2.0
SLCAN-FD、MiniCANFD 动态库和 Linux SocketCAN 之间复用同一套 CAN / CAN FD 收发逻辑。
Windows PCAN 后端用纯 Go（`syscall` 调用，无 CGO）封装 `PCANBasic.dll`；SLCAN
后端直接打开 `COMx`；Linux 后端使用内核 SocketCAN。

> 从 `v1.0.0` 起，公开 API 遵循语义化版本兼容承诺；破坏性变更只会在新的主版本中发布。

---

## 为什么做这个

- **Windows** 端 PEAK 官方仅提供 C/C++、C#、Java、Python 等绑定，**没有 Go 封装**
- **Linux** 端事实标准是内核 SocketCAN，接口通常是 `can0` / `vcan0`
- 现有的 Python 桥接（`can-bridge-win`）需要打包 `PyInstaller`、引入 `python-can` 依赖，
  在嵌入到机器人控制等纯 Go 项目时既笨重又难以追踪问题

`gocan` 的目标是提供稳定的跨平台 CAN 抽象层：Windows 可走 PCANBasic 或 SLCAN，
Linux 可走 SocketCAN 或 SLCAN，上层业务尽量只依赖统一的 `Bus` / `Frame` API。

---

## 特性

- ✅ Classical CAN 与 CAN FD 双标准支持
- ✅ 高层 `Bus` API：`Send` / `SendMany` / `Receive` / `ReadOne` / `TryRead` / `Status` / `Reset` / `SetFilter`
- ✅ 三种接收模式：`ModeAuto` / `ModePolling` / `ModeEvent`（Windows Event 驱动）
- ✅ 子包 `raw`：与 PCANBasic C API 1:1 对应的低层绑定
- ✅ 错误处理：位掩码语义 + `errors.Is` 哨兵
- ✅ Linux SocketCAN 后端：`Open(SocketCAN("can0"))` / `OpenFD(SocketCAN("vcan0"), "")` / `SetFilter`
- ✅ CANable 2.0 SLCAN-FD：`OpenSLCAN("COM5", ...)` / `OpenSLCANFD("COM5", ...)`
- ✅ MiniCANFD 厂商动态库：`LookupMiniCANFDDevices` / `OpenMiniCANFD`
- ✅ SLCAN 多通道合流：`BusGroup.AddSLCAN` / `BusGroup.AddSLCANFD`
- ✅ 通道发现与设备信息：`LookupChannels()` / `GetDeviceInfo()`
- ✅ 完整的中文文档与 15 个示例

详细范围见 [设计文档](docs/superpowers/specs/2026-05-22-gocan-design.md)。

---

## 快速开始

按你的平台跳转：

- 🐧 **Linux**（SocketCAN）→ [docs/quickstart-linux.md](docs/quickstart-linux.md)
- 🪟 **Windows**（PEAK PCAN / CANable 2.0）→ [docs/quickstart-windows.md](docs/quickstart-windows.md)
- 🔌 **CANable 2.0 SLCAN-FD** → [docs/slcan-windows.md](docs/slcan-windows.md)

完整 Option 总览：[docs/options.md](docs/options.md)

---

## 通道发现

```go
channels, err := gocan.LookupChannels()
if err != nil {
    log.Fatal(err)
}
for _, ch := range channels {
    info, err := gocan.GetDeviceInfo(ch.Channel)
    if err != nil {
        log.Printf("%s %s: %v", ch.Backend, ch.Name, err)
        continue
    }
    log.Printf("%s %s up=%v fd=%v hardware=%s", info.Backend, info.Name, info.Up, info.FD, info.HardwareName)
}
```

## Linux SocketCAN

```go
bus, err := gocan.Open(gocan.SocketCAN("can0"))
if err != nil {
    log.Fatal(err)
}
defer bus.Close()
```

CAN FD：

```go
bus, err := gocan.OpenFD(gocan.SocketCAN("vcan0"), "")
```

Linux 上 bitrate 由系统配置，不由 `WithBitrate` 设置，例如：

```bash
sudo ip link set can0 type can bitrate 500000
sudo ip link set can0 up
```

## MiniCANFD 厂商动态库

MiniCANFD 是 USB-CANFD 适配器后端，不会创建 `canN` 网卡，也不负责识别
L30/O20；上层仍可复用自己的 CAN FD 协议识别逻辑。Linux 使用 `libcanbus.so`
（aarch64 可使用 `libcanbus_arm64.so`），Windows amd64 使用 `HCanbus.dll`。

```go
devices, err := gocan.LookupMiniCANFDDevices("")
if err != nil {
    log.Fatal(err)
}
bus, err := gocan.OpenMiniCANFD(gocan.MiniCANFDConfig{
    DeviceIndex: devices[0].Index,
    Channel:     0,
    LibraryPath: os.Getenv("MINICANFD_LIBRARY_PATH"),
})
```

库路径也可通过 `MINICANFD_LIBRARY_PATH` 或 `LINKERBOT_CANFD_LIB` 指定。厂商
二进制不随 gocan 发布包提供，需由应用按目标平台分发。

## CANable 2.0 SLCAN-FD

```go
bus, err := gocan.OpenSLCANFD(
    "COM5",
    gocan.SLCANBitrate500K,
    gocan.SLCANDataBitrate2M,
)
```

该入口使用 CANable 2.0 官方 `canable2-fw` 的扩展协议，支持 `d/D` 无 BRS FD 帧、
`b/B` BRS FD 帧和最大 64 字节载荷。完整说明见
[Windows SLCAN-FD 文档](docs/slcan-windows.md)。

## 系统要求

- Windows PCAN：Go 1.22+、已安装 PEAK PCAN 驱动、`PCANBasic.dll` 与 Go 程序架构匹配
- Windows SLCAN：Go 1.22+、CANable 2.0 已刷入 SLCAN-FD 固件并出现 `COMx`
- Linux：Go 1.22+、内核启用 SocketCAN、已创建并启用 `can0` / `vcan0` 等网络接口
- macOS：串口 SLCAN 可用于真机；PCAN API 仅提供编译桩

---

## 版本状态

| 版本 | 主要内容 |
|---|---|
| v1.0.0 | 首个稳定版本：PCANBasic、SocketCAN、CANable 2.0 SLCAN-FD、BusGroup、通道发现与跨平台文档 |

---

## 许可证

[MIT](LICENSE)
