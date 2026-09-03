package gocan

// ChannelBackend 表示通道所属的底层后端。
type ChannelBackend string

const (
	// BackendPCAN 表示 Windows PCANBasic 后端。
	BackendPCAN ChannelBackend = "pcan"
	// BackendSocketCAN 表示 Linux SocketCAN 后端。
	BackendSocketCAN ChannelBackend = "socketcan"
	// BackendSLCAN 表示串口 SLCAN / CANable 2.0 SLCAN-FD 后端。
	BackendSLCAN ChannelBackend = "slcan"
	// BackendMiniCANFD 表示 MiniCANFD 厂商动态库后端。
	BackendMiniCANFD ChannelBackend = "minicanfd"
)

// ChannelInfo 描述一个可尝试打开的 CAN/CAN FD 通道。
type ChannelInfo struct {
	Channel Channel
	Name    string
	Backend ChannelBackend
	Up      bool
	FD      bool
}

// LookupChannels 返回当前平台可发现的 CAN/CAN FD 通道。
func LookupChannels() ([]ChannelInfo, error) {
	return lookupChannels()
}
