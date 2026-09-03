package gocan

import (
	"context"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/zhuzx17/gocan/raw"
)

// Bus 表示一个已初始化的 CAN/CAN FD 通道。
//
// 必须使用 Close 释放资源。Close 是幂等的，可以从多个 goroutine 安全调用。
type Bus struct {
	ch        raw.TPCANHandle
	adapt     rawAdapter
	slcan     *slcanBackend
	minicanfd *miniCANFDBackend
	cfg       *config
	isFD      bool

	// 接收侧。reader goroutine 在阶段 4 引入。
	rxCh    chan Frame
	errCh   chan error
	closing chan struct{}

	// Event 模式相关（Windows 真实生效；其他平台为 0）。
	// 用 uintptr 而不是 windows.Handle，以保持类型跨平台一致。
	// nolint 提示：这两个字段仅在 event_windows.go 里被读写，
	// Linux 构建看不到那些引用——这是构建标签的正常代价，不是死代码。
	eventHandle uintptr //nolint:unused // 仅 windows 构建使用
	abortHandle uintptr //nolint:unused // 仅 windows 构建使用
	useEvent    bool    // 实际启用了 Event 模式（非 ModeEvent 显式 + 也包含 Auto 升级）

	// 关闭管理。
	closeOnce sync.Once
	closed    atomic.Bool
}

// Open 打开一个 Classical CAN 通道。
//
// ch 是通道句柄（USBBus1..USBBus16 等）；
// opts 用于配置波特率、接收模式、buffer 大小、Logger 等。
func Open(ch Channel, opts ...Option) (*Bus, error) {
	return openWith(liveAdapter{}, ch, false, "", opts...)
}

// OpenFD 打开一个 CAN FD 通道。
//
// fdBitrate 是 PCAN 官方格式的字符串，详见 docs/can-fd.md。
// WithBitrate 对 OpenFD 无效（FD 波特率完全由 fdBitrate 决定）。
func OpenFD(ch Channel, fdBitrate string, opts ...Option) (*Bus, error) {
	return openWith(liveAdapter{}, ch, true, fdBitrate, opts...)
}

// openWith 是 Open/OpenFD 的内部入口，注入 rawAdapter 以便测试。
func openWith(adapt rawAdapter, ch Channel, fd bool, fdBitrate string, opts ...Option) (*Bus, error) {
	cfg := newDefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	var status raw.TPCANStatus
	if fd {
		status = adapt.InitializeFD(ch, fdBitrate)
	} else {
		status = adapt.Initialize(ch, cfg.bitrate)
	}
	if status != raw.PCAN_ERROR_OK {
		op := "CAN_Initialize"
		if fd {
			op = "CAN_InitializeFD"
		}
		return nil, wrapStatus(adapt, op, status)
	}

	b := &Bus{
		ch:      ch,
		adapt:   adapt,
		cfg:     cfg,
		isFD:    fd,
		rxCh:    make(chan Frame, cfg.rxBufferSize),
		errCh:   make(chan error, cfg.errBufferSize),
		closing: make(chan struct{}),
	}

	if err := applyPlatformOptions(b, cfg); err != nil {
		_ = adapt.Uninitialize(ch)
		return nil, err
	}

	// 根据 ReceiveMode 决定接收路径。
	// ModePolling：直接 Polling。
	// ModeEvent：必须成功；失败则 Uninitialize 后返回错误。
	// ModeAuto：先尝试 Event，失败则降级到 Polling 并打日志。
	switch cfg.receiveMode {
	case ModePolling:
		// 不调用 setupEventMode，useEvent 保持 false。
	case ModeEvent:
		if err := b.setupEventMode(); err != nil {
			_ = adapt.Uninitialize(ch)
			return nil, err
		}
	case ModeAuto:
		if err := b.setupEventMode(); err != nil {
			cfg.logger.Infof("event mode unavailable, falling back to polling: %v", err)
		}
	}

	b.startReader()
	return b, nil
}

// ReadOne 阻塞从接收队列取一帧，直到 ctx 取消或 Bus 关闭。
//
// Bus 关闭时返回 ErrBusClosed。
// ctx 被取消时返回 ctx.Err()。
func (b *Bus) ReadOne(ctx context.Context) (Frame, error) {
	select {
	case f, ok := <-b.rxCh:
		if !ok {
			return Frame{}, ErrBusClosed
		}
		return f, nil
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	}
}

// TryRead 非阻塞读一帧。队列空时返回 ErrQueueEmpty；Bus 已关闭返回 ErrBusClosed。
func (b *Bus) TryRead() (Frame, error) {
	select {
	case f, ok := <-b.rxCh:
		if !ok {
			return Frame{}, ErrBusClosed
		}
		return f, nil
	default:
		return Frame{}, ErrQueueEmpty
	}
}

// Send 同步发送一帧。Close 后调用返回 ErrBusClosed。
//
// ctx 在 Send 入口被一次性检查（Send 自身是同步调用，不会阻塞太久），
// 真正长时阻塞应通过 SendMany 或上层超时机制控制。
func (b *Bus) Send(ctx context.Context, f Frame) error {
	if b.closed.Load() {
		return ErrBusClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !b.isFD && f.Has(FlagFD) {
		return ErrFDNotSupportedOnBus
	}
	if b.slcan != nil {
		return b.slcan.send(f)
	}
	if b.minicanfd != nil {
		return b.minicanfd.send(f)
	}

	if f.Has(FlagFD) {
		m := toRawMsgFD(f)
		if s := b.adapt.WriteFD(b.ch, &m); s != raw.PCAN_ERROR_OK {
			return wrapStatus(b.adapt, "CAN_WriteFD", s)
		}
		return nil
	}
	m := toRawMsg(f)
	if s := b.adapt.Write(b.ch, &m); s != raw.PCAN_ERROR_OK {
		return wrapStatus(b.adapt, "CAN_Write", s)
	}
	return nil
}

// SendMany 顺序逐帧发送，任意一帧失败立即返回 *SendManyError。
//
// 已成功发送的帧不会回滚 —— CAN 总线无事务概念。
// ctx 在每帧前检查一次，便于及时取消大批量发送。
func (b *Bus) SendMany(ctx context.Context, frames []Frame) error {
	for i, f := range frames {
		if err := ctx.Err(); err != nil {
			return &SendManyError{Index: i, Frame: copyFrame(f), Err: err}
		}
		if err := b.Send(ctx, f); err != nil {
			return &SendManyError{Index: i, Frame: copyFrame(f), Err: err}
		}
	}
	return nil
}

// Status 查询通道当前状态。
//
// 返回的 Status 是位掩码，使用 StatusHas 判断具体位。
// CANable 2.0 SLCAN-FD 固件不提供可安全用于帧流的状态查询，返回 ErrNotSupported。
func (b *Bus) Status() (Status, error) {
	if b.closed.Load() {
		return 0, ErrBusClosed
	}
	if b.slcan != nil {
		return 0, ErrNotSupported
	}
	if b.minicanfd != nil {
		return 0, ErrNotSupported
	}
	return Status(b.adapt.GetStatus(b.ch)), nil
}

// Reset 复位通道。PCAN/SocketCAN 清空底层收发队列；SLCAN 发送 C/O 重开通道。
// 通常用于 BUSOFF 恢复。
func (b *Bus) Reset() error {
	if b.closed.Load() {
		return ErrBusClosed
	}
	if b.slcan != nil {
		return b.slcan.reset()
	}
	if b.minicanfd != nil {
		return b.minicanfd.reset()
	}
	if s := b.adapt.Reset(b.ch); s != raw.PCAN_ERROR_OK {
		return wrapStatus(b.adapt, "CAN_Reset", s)
	}
	return nil
}

// SetFilter 设置消息过滤范围（PCAN 默认是开放的，本方法用于收窄）。
//
// idMin/idMax 是要接受的 ID 闭区间；mode 区分 11 位 / 29 位 ID。
// 多次调用会累加：要恢复"接收全部"应调用 ResetFilter。
func (b *Bus) SetFilter(idMin, idMax uint32, mode FilterMode) error {
	if b.closed.Load() {
		return ErrBusClosed
	}
	if b.slcan != nil {
		return ErrNotSupported
	}
	if b.minicanfd != nil {
		return ErrNotSupported
	}
	var mt raw.TPCANMessageType
	if mode == FilterExtended {
		mt = raw.PCAN_MESSAGE_EXTENDED
	}
	if s := b.adapt.FilterMessages(b.ch, idMin, idMax, mt); s != raw.PCAN_ERROR_OK {
		return wrapStatus(b.adapt, "CAN_FilterMessages", s)
	}
	return nil
}

// ResetFilter 恢复"接收全部"。
// 实现方式：通过 SetValue(PCAN_MESSAGE_FILTER, PCAN_FILTER_OPEN) 打开滤波器。
func (b *Bus) ResetFilter() error {
	if b.closed.Load() {
		return ErrBusClosed
	}
	if b.slcan != nil {
		return ErrNotSupported
	}
	v := uint32(raw.PCAN_FILTER_OPEN)
	s := b.adapt.SetValue(b.ch, raw.PCAN_MESSAGE_FILTER,
		unsafe.Pointer(&v), uint32(unsafe.Sizeof(v)))
	if s != raw.PCAN_ERROR_OK {
		return wrapStatus(b.adapt, "CAN_SetValue(MESSAGE_FILTER=OPEN)", s)
	}
	return nil
}

// Close 释放底层通道或串口。幂等：多次调用安全。
//
// 流程：标记 closed → 关闭 closing → 触发 abort event 唤醒 reader →
// 等 reader 关闭 rxCh → 释放 event 句柄 → Uninitialize → 关闭 errCh。
func (b *Bus) Close() error {
	var err error
	b.closeOnce.Do(func() {
		b.closed.Store(true)
		close(b.closing)
		if b.slcan != nil {
			// Closing the port wakes a serial Read that may currently be blocked.
			err = b.slcan.close()
			for range b.rxCh {
			}
			close(b.errCh)
			return
		}
		if b.minicanfd != nil {
			err = b.minicanfd.close()
			for range b.rxCh {
			}
			close(b.errCh)
			return
		}
		// Event 模式下需要 SetEvent 唤醒阻塞在 WaitForMultipleObjects 的 reader。
		b.closeEventMode()
		// 等 reader goroutine 退出 —— 它在退出时关闭 rxCh。
		for range b.rxCh {
			// 丢弃剩余帧，避免 reader 阻塞在 rxCh 发送。
		}
		// reader 已退出，可以安全 CloseHandle。
		b.finalizeEventMode()
		if s := b.adapt.Uninitialize(b.ch); s != raw.PCAN_ERROR_OK {
			err = wrapStatus(b.adapt, "CAN_Uninitialize", s)
		}
		close(b.errCh)
	})
	return err
}

// Receive 返回流式接收 channel。Bus 关闭时 channel 也关闭。
//
// 推荐用法：在专门的 goroutine 里 `for f := range bus.Receive()`。
func (b *Bus) Receive() <-chan Frame { return b.rxCh }

// Errors 返回接收侧的异步错误流。Bus 关闭时 channel 也关闭。
// QRCVEMPTY（队列空）不会出现在这里 —— 它是正常状态，不算错误。
func (b *Bus) Errors() <-chan error { return b.errCh }

// wrapStatus 把 raw 状态码包成 *Error，附带 GetErrorText 取到的描述。
func wrapStatus(adapt rawAdapter, op string, code raw.TPCANStatus) *Error {
	msg, _ := adapt.GetErrorText(code, raw.LanguageEnglish)
	return &Error{Code: code, Op: op, Msg: msg}
}

// copyFrame 深拷贝 Frame.Data 用于 SendManyError 持久持有。
func copyFrame(f Frame) Frame {
	out := f
	if f.Data != nil {
		out.Data = append([]byte(nil), f.Data...)
	}
	return out
}

// toRawMsg 把 Classical Frame 转成底层 TPCANMsg。
func toRawMsg(f Frame) raw.TPCANMsg {
	var mt raw.TPCANMessageType
	if f.Has(FlagExtended) {
		mt |= raw.PCAN_MESSAGE_EXTENDED
	}
	if f.Has(FlagRemote) {
		mt |= raw.PCAN_MESSAGE_RTR
	}
	m := raw.TPCANMsg{
		ID:      f.ID,
		MsgType: mt,
		Len:     uint8(len(f.Data)),
	}
	copy(m.Data[:], f.Data)
	return m
}

// toRawMsgFD 把 FD Frame 转成底层 TPCANMsgFD。
func toRawMsgFD(f Frame) raw.TPCANMsgFD {
	var mt raw.TPCANMessageType
	mt |= raw.PCAN_MESSAGE_FD
	if f.Has(FlagExtended) {
		mt |= raw.PCAN_MESSAGE_EXTENDED
	}
	if f.Has(FlagBRS) {
		mt |= raw.PCAN_MESSAGE_BRS
	}
	if f.Has(FlagESI) {
		mt |= raw.PCAN_MESSAGE_ESI
	}
	m := raw.TPCANMsgFD{
		ID:      f.ID,
		MsgType: mt,
		DLC:     dataLenToDLC(len(f.Data)),
	}
	copy(m.Data[:], f.Data)
	return m
}

// dataLenToDLC 把字节长度转成 FD 协议的 DLC 编码（0..15）。
func dataLenToDLC(n int) uint8 {
	switch n {
	case 0, 1, 2, 3, 4, 5, 6, 7, 8:
		return uint8(n)
	case 12:
		return 9
	case 16:
		return 10
	case 20:
		return 11
	case 24:
		return 12
	case 32:
		return 13
	case 48:
		return 14
	case 64:
		return 15
	}
	return 0
}

// dlcToDataLen 把 FD 的 DLC 编码反解出字节长度。
func dlcToDataLen(d uint8) int {
	switch d {
	case 9:
		return 12
	case 10:
		return 16
	case 11:
		return 20
	case 12:
		return 24
	case 13:
		return 32
	case 14:
		return 48
	case 15:
		return 64
	default:
		if d <= 8 {
			return int(d)
		}
		return 0
	}
}
