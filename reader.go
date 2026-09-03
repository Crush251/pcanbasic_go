package gocan

import (
	"errors"
	"time"

	"github.com/zhuzx17/gocan/raw"
)

// errQueueEmpty 是内部信号：底层报告"队列空"。
// 仅 reader 内部使用，不会暴露给 Errors() channel；
// 用户层应使用 ErrQueueEmpty（仅 TryRead 会返回它）。
var errQueueEmpty = errors.New("internal: queue empty")

// startReader 启动后台 reader goroutine。
//
// 单 reader 设计：所有底层 Read/ReadFD 调用由它独占，
// 避免多个调用方竞争 PCAN 内部接收队列。
func (b *Bus) startReader() {
	go b.readerLoop()
}

// readerLoop 是 reader goroutine 的主循环。
//
// 不变量：退出前必关闭 rxCh —— Close 流程靠这个信号确认 reader 已退出。
func (b *Bus) readerLoop() {
	defer close(b.rxCh)
	for {
		if !b.waitForData() {
			return
		}
		// 一次 wait 信号到来后，尽量把队列里的帧全部取出再回去等。
		for {
			f, err := b.readOnce()
			if errors.Is(err, errQueueEmpty) {
				break
			}
			if err != nil {
				select {
				case <-b.closing:
					return
				default:
				}
				select {
				case b.errCh <- err:
				default: // errCh 满则丢弃（按 spec §3.5：errCh 是"提示性"通道）
				}
				break
			}
			select {
			case b.rxCh <- f:
			case <-b.closing:
				return
			}
		}
	}
}

// waitForData 根据接收模式等待"有数据"信号。
//
// 返回 false 表示要退出（closing 已关闭）。
// Event 模式（Windows）阻塞在 WaitForMultipleObjects；Polling 模式按周期轮询。
func (b *Bus) waitForData() bool {
	select {
	case <-b.closing:
		return false
	default:
	}

	if b.useEvent {
		return b.waitEventOrAbort()
	}

	timer := time.NewTimer(b.cfg.pollInterval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-b.closing:
		return false
	}
}

// readOnce 从底层读一帧。队列空返回 errQueueEmpty（内部信号）。
func (b *Bus) readOnce() (Frame, error) {
	if b.slcan != nil {
		return b.slcan.readFrame()
	}
	if b.minicanfd != nil {
		return b.minicanfd.readFrame()
	}
	if b.isFD {
		return b.readOnceFD()
	}
	return b.readOnceClassical()
}

func (b *Bus) readOnceClassical() (Frame, error) {
	var (
		m  raw.TPCANMsg
		ts raw.TPCANTimestamp
	)
	s := b.adapt.Read(b.ch, &m, &ts)
	if s == raw.PCAN_ERROR_QRCVEMPTY {
		return Frame{}, errQueueEmpty
	}
	if s != raw.PCAN_ERROR_OK {
		return Frame{}, wrapStatus(b.adapt, "CAN_Read", s)
	}
	return Frame{
		ID:              m.ID,
		Data:            append([]byte(nil), m.Data[:m.Len]...),
		Flags:           msgTypeToFlags(m.MsgType),
		TimestampMicros: timestampToMicros(ts),
		ReceivedAt:      time.Now(),
	}, nil
}

func (b *Bus) readOnceFD() (Frame, error) {
	var (
		m  raw.TPCANMsgFD
		ts raw.TPCANTimestampFD
	)
	s := b.adapt.ReadFD(b.ch, &m, &ts)
	if s == raw.PCAN_ERROR_QRCVEMPTY {
		return Frame{}, errQueueEmpty
	}
	if s != raw.PCAN_ERROR_OK {
		return Frame{}, wrapStatus(b.adapt, "CAN_ReadFD", s)
	}
	n := dlcToDataLen(m.DLC)
	return Frame{
		ID:              m.ID,
		Data:            append([]byte(nil), m.Data[:n]...),
		Flags:           msgTypeToFlags(m.MsgType),
		TimestampMicros: uint64(ts),
		ReceivedAt:      time.Now(),
	}, nil
}

// msgTypeToFlags 把底层 MsgType 位掩码转成上层 FrameFlags。
func msgTypeToFlags(mt raw.TPCANMessageType) FrameFlags {
	var f FrameFlags
	if mt&raw.PCAN_MESSAGE_EXTENDED != 0 {
		f |= FlagExtended
	}
	if mt&raw.PCAN_MESSAGE_RTR != 0 {
		f |= FlagRemote
	}
	if mt&raw.PCAN_MESSAGE_FD != 0 {
		f |= FlagFD
	}
	if mt&raw.PCAN_MESSAGE_BRS != 0 {
		f |= FlagBRS
	}
	if mt&raw.PCAN_MESSAGE_ESI != 0 {
		f |= FlagESI
	}
	return f
}

// timestampToMicros 把 Classical 的 TPCANTimestamp 合成总微秒数。
//
// PCAN Classical 时间戳的语义：
//   - Millis：自驱动启动起的毫秒数（uint32 会绕回）
//   - MillisOverflow：上面 Millis 绕回的次数
//   - Micros：毫秒内的微秒偏移（0..999）
func timestampToMicros(ts raw.TPCANTimestamp) uint64 {
	// 一次完整的 Millis 周期 = 2^32 毫秒。
	const millisCycle uint64 = uint64(1) << 32
	totalMillis := uint64(ts.MillisOverflow)*millisCycle + uint64(ts.Millis)
	return totalMillis*1000 + uint64(ts.Micros)
}
