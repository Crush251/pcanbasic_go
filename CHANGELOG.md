# Changelog

本项目的所有显著变更将记录在此文件。

文件格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本 2.0.0](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [1.1.0] - 2026-09-03

### Added

- MiniCANFD 厂商动态库后端：支持 Linux `libcanbus.so` /
  `libcanbus_arm64.so` 与 Windows amd64 `HCanbus.dll`，提供
  `LookupMiniCANFDDevices` / `OpenMiniCANFD`。
- MiniCANFD 设备发现、CAN/CAN FD 帧收发、过滤器和状态重置，并映射 FD、BRS、扩展帧与
  RTR 等标志。

### Changed

- MiniCANFD 动态库按路径在进程内共享引用；首次打开时调用可用的
  `LibCANbus_Init`，最后一个总线关闭时调用 `LibCANbus_Exit`。
- MiniCANFD 打开失败会回滚已完成的初始化；`Bus.Close` 会释放通道和动态库引用，并传播
  厂商清理错误。
- 应用重连 MiniCANFD 时应完整关闭旧 `Bus`，再重新调用
  `LookupMiniCANFDDevices` / `OpenMiniCANFD`；不要重复调用 `CAN_OpenDevice` 代替重连。

## [1.0.0] - 2026-07-24

### Fixed

- macOS 无 CGO 交叉编译不再依赖串口库的 IOKit 详细枚举；`LookupSLCANPorts()`
  在该平台回退到基础端口列表。
- Linux SocketCAN 后端发送 CAN FD 帧时未置 `CANFD_FDF` 标志，导致内核按
  Classical CAN 处理，>8 字节 payload 被截断/丢弃。修复后所有 FD 帧发送均
  正确带 FDF 位；BRS / ESI 仍按 `Frame.Flags` 可选叠加。
- `ReadFD` / `readFDWithTimestamp` 分支放宽：现在按"标准 FD 帧长度 或
  长度足够且携带 FDF 标志"识别 FD 帧，避免混合总线场景下静默丢帧。

### Changed

- 仓库迁移到 `github.com/zhuzx17/gocan`，module path 同步更新。所有
  `import "github.com/Crush251/gocan"` 需改为 `import "github.com/zhuzx17/gocan"`。
  GitHub 在一段时间内会保留旧 URL 的 redirect，但建议尽快更新。

### Added

- CANable 2.0 SLCAN-FD 串口后端：`OpenSLCAN` / `OpenSLCANFD`、
  `BusGroup.AddSLCAN` / `BusGroup.AddSLCANFD`，支持 Classical、Extended、RTR、
  CAN FD、BRS 和最大 64 字节载荷。
- SLCAN 串口发现：`LookupSLCANPorts()`，可通过官方 VID:PID `16D0:117E` 识别
  CANable 2.0；新增静默模式、自动重传和串口速率选项。
- BusGroup：按业务名字管理多个 `*Bus`，提供合流接收 (`Receive() <-chan SourcedFrame`)、聚合关闭 (`*GroupCloseError`)、`Each` 遍历等方法。
- Linux SocketCAN 自定义参数：`WithLoopback` / `WithRecvOwnMsgs` / `WithErrFilter` / `WithJoinFilters` / `WithRecvTimestamp` / `WithSocketBuffers` / `WithRWTimeout`。
- 运行期方法：`(b *Bus) SetErrFilter(uint32)` / `SetJoinFilters(bool)`，Linux 真实生效，其它平台返回 `ErrNotSupported`。
- 5 个新示例：`examples/11_busgroup_socketcan` / `12_busgroup_fan_in` /
  `13_socketcan_loopback` / `14_socketcan_advanced` / `15_canable_slcan_fd`。
- 工具：`scripts/setup-vcan.sh` 一键创建 / 销毁 vcan 接口；`justfile` 别名 `vcan-up` / `vcan-down`。

### Documentation

- 拆分 `docs/quickstart.md` → `docs/quickstart-linux.md` + `docs/quickstart-windows.md`，
  各自独立讲清 Classical 与 CAN FD 的 5 分钟启动流程。
- 新增 `docs/options.md`：所有 `WithXxx` Option 集中速查表 + 平台标注 + 详细行为说明。
- 新增 `docs/socketcan-options.md`：Linux 专属 Option 深度阅读（每项对应 setsockopt 名 + 内核版本要求）。
- `docs/troubleshooting.md` 增加「平台对照速查」段：常见失败按 Windows / Linux 分别给出根因。

## [0.1.0] - 2026-05-22

首个公开版本：Windows 上的 PEAK PCANBasic.dll Go 封装。

### Added

**底层绑定（`raw` 子包）**

- Windows 通过 `golang.org/x/sys/windows` 直接 syscall 加载 `PCANBasic.dll`，零 cgo
- 非 Windows 平台 stub 实现：编译通过、API 返回 `PCAN_ERROR_ILLOPERATION`
- 完整类型映射：`TPCANHandle` / `TPCANStatus` / `TPCANBaudrate` / `TPCANMessageType` / `TPCANParameter` / `TPCANMsg` / `TPCANMsgFD` / `TPCANTimestamp(FD)`
- 13 个 PCAN API 函数绑定：`Initialize` / `InitializeFD` / `Uninitialize` / `Read` / `ReadFD` / `Write` / `WriteFD` / `GetStatus` / `Reset` / `FilterMessages` / `GetValue` / `SetValue` / `GetErrorText`
- 完整常量：USB/PCI/LAN 通道、波特率、错误码、消息类型、参数键、过滤器模式
- DLL 路径可通过环境变量 `PCANBASIC_DLL_PATH` 覆盖

**顶层 idiomatic API**

- `Bus`：表示一个已初始化的通道；`Open` / `OpenFD` 构造，`Close` 释放（幂等）
- `Frame`：统一 Classical/FD 表示，附带 `FrameFlags`（Extended/Remote/FD/BRS/ESI）、`TimestampMicros`、`ReceivedAt`
- 构造器：`NewFrame` / `NewExtendedFrame` / `NewRemoteFrame` / `NewFDFrame`，含 ID 范围、数据长度、FD 离散长度校验
- 发送：`Send(ctx, frame)` / `SendMany(ctx, frames)`；批量失败返回 `*SendManyError`（含失败 index、深拷贝帧、内部错误）
- 接收：`Receive() <-chan Frame` 流式、`ReadOne(ctx)` 阻塞取一帧、`TryRead()` 非阻塞取一帧、`Errors() <-chan error` 异步错误
- 状态与恢复：`Status()`、`Reset()`、`StatusHas(s, bit)`
- 过滤器：`SetFilter(idMin, idMax, mode)` / `ResetFilter()`
- 选项：`WithBitrate` / `WithReceiveMode` / `WithPollInterval` / `WithRxBufferSize` / `WithErrBufferSize` / `WithLogger`

**接收模式**

- `ModePolling`：周期轮询底层 Read，跨平台
- `ModeEvent`：Windows 上 `CreateEvent` + `WaitForMultipleObjects`，低延迟低 CPU
- `ModeAuto`（默认）：尝试 Event，失败降级 Polling 并通过 Logger 提示

**错误模型**

- `*Error{Code, Op, Msg}` 保留原始 PCAN 位掩码；`Is(target)` 支持位匹配，允许同一错误同时匹配多个哨兵
- 完整哨兵错误：`ErrBusClosed` / `ErrFDNotSupportedOnBus` / `ErrIDOutOfRange` / `ErrDataTooLong` / `ErrInvalidFDLength` / `ErrRemoteOnFD` / `ErrNotSupported` / `ErrDLLNotFound` / `ErrQueueEmpty` / `ErrQueueOverrun` / `ErrQueueXmtFull` / `ErrBusLight` / `ErrBusHeavy` / `ErrBusPassive` / `ErrBusOff` / `ErrNotInitialized` / `ErrIllHandle` / `ErrIllParamType` / `ErrIllParamValue` / `ErrIllOperation` / `ErrNoDriver` / `ErrUnknown`
- `SendManyError` 实现 `Unwrap`，`errors.Is/As` 自然穿透

**并发与生命周期**

- 单 reader goroutine 独占底层 Read，多 goroutine 可安全并发 Send/Status/Reset/Receive
- Close 流程：标记 closed → 关闭 closing → 唤醒 reader → 等 rxCh 关闭 → 释放 event 句柄 → Uninitialize → 关闭 errCh
- `sync.Once` 保证 Close 幂等

**示例（`examples/`）**

10 个独立示例，每个 ≤ 100 行带中文头注释：发送 Classical / Polling 接收 / Event 接收 / 发送 FD / 接收 FD / 多通道 / 过滤器 / Status+Reset / slog 适配 / 直接调 raw 包

**文档（`docs/`）**

7 篇：`quickstart` / `architecture` / `can-fd` / `error-handling` / `platform-support` / `hardware-test-setup` / `troubleshooting`

**测试与 CI**

- fake adapter + 错误注入 adapter 覆盖所有公开 API
- 测试覆盖率 80%+，`-race` 通过
- GitHub Actions：Linux runner 跑 vet + golangci-lint + race test + 跨平台编译（windows/amd64+386）；Windows runner 跑 vet + 普通 test

[Unreleased]: https://github.com/zhuzx17/gocan/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/zhuzx17/gocan/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/zhuzx17/gocan/releases/tag/v1.0.0
[0.1.0]: https://github.com/zhuzx17/gocan/releases/tag/v0.1.0
