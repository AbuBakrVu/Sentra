package netprobe

import (
	"net"
	"os"
	"syscall"
)

// openDgram открывает ICMP-сокет без прав root. Работает, если группа процесса
// входит в net.ipv4.ping_group_range (в контейнерах Docker так по умолчанию).
func openDgram() (net.PacketConn, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, syscall.IPPROTO_ICMP)
	if err != nil {
		return nil, err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{}); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "icmp")
	defer f.Close()
	return net.FilePacketConn(f)
}
