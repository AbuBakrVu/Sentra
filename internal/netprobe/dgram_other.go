//go:build !linux

package netprobe

import (
	"errors"
	"net"
)

func openDgram() (net.PacketConn, error) { return nil, errors.New("только Linux") }
