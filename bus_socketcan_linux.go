//go:build linux

package gocan

import "github.com/zhuzx17/gocan/raw"

// SetErrFilter 运行期更新 CAN_RAW_ERR_FILTER 掩码。
// setsockopt 失败时返回错误，linuxChannel 里持久化的 mask 保持原值（与内核状态一致）。
func (b *Bus) SetErrFilter(mask uint32) error {
	if b.closed.Load() {
		return ErrBusClosed
	}
	if b.minicanfd != nil {
		return ErrNotSupported
	}
	if s := raw.SetCANRawErrFilter(b.ch, mask); s != raw.PCAN_ERROR_OK {
		return wrapStatus(b.adapt, "setsockopt(CAN_RAW_ERR_FILTER)", s)
	}
	return nil
}

// SetJoinFilters 运行期更新 CAN_RAW_JOIN_FILTERS（true=AND, false=OR）。
// setsockopt 失败时返回错误，linuxChannel 里持久化的标志保持原值。
func (b *Bus) SetJoinFilters(and bool) error {
	if b.closed.Load() {
		return ErrBusClosed
	}
	if b.minicanfd != nil {
		return ErrNotSupported
	}
	if s := raw.SetCANRawJoinFilters(b.ch, and); s != raw.PCAN_ERROR_OK {
		return wrapStatus(b.adapt, "setsockopt(CAN_RAW_JOIN_FILTERS)", s)
	}
	return nil
}
