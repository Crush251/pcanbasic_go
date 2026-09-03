package gocan

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

func TestMiniCANFDDLCMapping(t *testing.T) {
	for n := 0; n <= 8; n++ {
		dlc, ok := dataLenToMiniCANFDDLC(n)
		if !ok || int(dlc) != n {
			t.Fatalf("length %d mapped to DLC %d, ok=%v", n, dlc, ok)
		}
	}
	for dlc, want := range map[uint8]int{9: 12, 10: 16, 11: 20, 12: 24, 13: 32, 14: 48, 15: 64} {
		got, ok := miniCANFDDLCToLength(dlc)
		if !ok || got != want {
			t.Fatalf("DLC %d mapped to length %d, ok=%v", dlc, got, ok)
		}
	}
	if _, ok := dataLenToMiniCANFDDLC(9); ok {
		t.Fatal("non-canonical FD length 9 must be rejected")
	}
}

func TestMiniCANFDFrameConversion(t *testing.T) {
	in := Frame{ID: 0x123, Data: make([]byte, 12), Flags: FlagFD | FlagExtended | FlagBRS}
	for i := range in.Data {
		in.Data[i] = byte(i)
	}
	msg, err := miniCANFDFrameFromFrame(in, MiniCANFDDefaultFrameType)
	if err != nil {
		t.Fatal(err)
	}
	if msg.DLC != 9 || msg.ExternFlag != 1 || msg.FrameType != 0x0c {
		t.Fatalf("unexpected vendor frame: %+v", msg)
	}
	out, err := frameFromMiniCANFDMsg(msg)
	if err != nil || out.ID != in.ID || string(out.Data) != string(in.Data) || !out.Has(FlagExtended) || !out.Has(FlagFD) || !out.Has(FlagBRS) {
		t.Fatalf("round trip failed: frame=%+v err=%v", out, err)
	}
}

func TestMiniCANFDFrameConversionClassicalAndBRS(t *testing.T) {
	msg, err := miniCANFDFrameFromFrame(Frame{ID: 1, Data: []byte{1, 2}, Flags: FlagFD}, MiniCANFDDefaultFrameType)
	if err != nil || msg.FrameType != 0x04 {
		t.Fatalf("FD frame type = 0x%02x, err=%v", msg.FrameType, err)
	}
	msg, err = miniCANFDFrameFromFrame(Frame{ID: 1, Data: []byte{1, 2}, Flags: FlagFD | FlagBRS}, MiniCANFDDefaultFrameType)
	if err != nil || msg.FrameType != 0x0c {
		t.Fatalf("BRS frame type = 0x%02x, err=%v", msg.FrameType, err)
	}
	msg, err = miniCANFDFrameFromFrame(Frame{ID: 1, Data: []byte{1, 2}}, MiniCANFDDefaultFrameType)
	if err != nil || msg.FrameType != 0 {
		t.Fatalf("classical frame type = 0x%02x, err=%v", msg.FrameType, err)
	}
}

func TestMiniCANFDBackendLifecycle(t *testing.T) {
	var sent, closed int
	queue := []miniCANFDMsg{{ID: 7, DLC: 1, Data: [64]uint8{42}}}
	lib := &miniCANFDLib{
		transmit: func(_ uint32, _ uint32, msg *miniCANFDMsg, count uint32, _ int32) int32 {
			sent += int(count)
			return int32(count)
		},
		receive: func(_ uint32, _ uint32, msg *miniCANFDMsg, _ uint32, _ int32) int32 {
			if len(queue) == 0 {
				return 0
			}
			*msg = queue[0]
			queue = queue[1:]
			return 1
		},
		closeDevice: func(_, _ uint32) int32 { closed++; return 0 },
	}
	b := &Bus{minicanfd: &miniCANFDBackend{lib: lib, frameType: MiniCANFDDefaultFrameType}, isFD: true}
	if err := b.Send(context.Background(), Frame{ID: 1, Data: []byte{1}, Flags: FlagFD}); err != nil {
		t.Fatal(err)
	}
	f, err := b.minicanfd.readFrame()
	if err != nil || f.ID != 7 || f.Data[0] != 42 {
		t.Fatalf("read failed: %+v %v", f, err)
	}
	if err := b.minicanfd.close(); err != nil || sent != 1 || closed != 1 {
		t.Fatalf("close/send counters: sent=%d closed=%d err=%v", sent, closed, err)
	}
	if !errors.Is(b.minicanfd.send(Frame{Flags: FlagFD}), ErrBusClosed) {
		t.Fatal("send after close should fail")
	}
}

func TestMiniCANFDResetRestoresReceiveFilter(t *testing.T) {
	var resetCalls, filterCalls int
	lib := &miniCANFDLib{
		reset: func(_, _ uint32) int32 { resetCalls++; return 0 },
		setFilter: func(_ uint32, _ uint32, number, typ int8, id, mask uint32, enable int8) int32 {
			if number != 0 || typ != 1 || id != 0 || mask != 0 || enable != 1 {
				t.Fatalf("unexpected filter: number=%d type=%d id=%d mask=%d enable=%d", number, typ, id, mask, enable)
			}
			filterCalls++
			return 0
		},
		closeDevice: func(_, _ uint32) int32 { return 0 },
	}
	b := &Bus{minicanfd: &miniCANFDBackend{lib: lib}, isFD: true}
	if err := b.Reset(); err != nil {
		t.Fatal(err)
	}
	if resetCalls != 1 || filterCalls != 1 {
		t.Fatalf("reset calls = %d, filter calls = %d; want 1/1", resetCalls, filterCalls)
	}
}

func TestMiniCANFDRuntimeReferenceCounting(t *testing.T) {
	var exits atomic.Int32
	key := "test-runtime-reference-counting"
	runtime := &miniCANFDRuntime{
		lib:         &miniCANFDLib{runtimeExit: func() int32 { exits.Add(1); return 0 }},
		refs:        2,
		initialized: true,
	}
	miniCANFDRuntimes.Lock()
	miniCANFDRuntimes.byPath[key] = runtime
	miniCANFDRuntimes.Unlock()
	releaseMiniCANFDRuntime(key, runtime)
	if got := exits.Load(); got != 0 {
		t.Fatalf("runtime exit after first release = %d, want 0", got)
	}
	releaseMiniCANFDRuntime(key, runtime)
	if got := exits.Load(); got != 1 {
		t.Fatalf("runtime exit after final release = %d, want 1", got)
	}
	// A duplicate release is harmless and must not call the vendor again.
	releaseMiniCANFDRuntime(key, runtime)
	if got := exits.Load(); got != 1 {
		t.Fatalf("runtime exit after duplicate release = %d, want 1", got)
	}
}
