package netprobe

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestChecksum(t *testing.T) {
	b := echo(0x1234, 7, true)
	if checksum(b) != 0 {
		t.Fatalf("контрольная сумма готового пакета должна сворачиваться в 0, получили %#x", checksum(b))
	}
}

func TestLoss(t *testing.T) {
	if l := (Result{Sent: 5, Recv: 4}).Loss(); l != 20 {
		t.Fatalf("потери %v", l)
	}
	if l := (Result{}).Loss(); l != 100 {
		t.Fatalf("без отправок потери должны быть 100, получили %v", l)
	}
}

// Отказ в соединении — тоже признак живого адреса; молчание — нет.
func TestTCPAlive(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ip := net.IPv4(127, 0, 0, 1)
	if _, ok := tcpAlive(ctx, ip, []int{port}); !ok {
		t.Fatal("открытый порт должен считаться живым")
	}
	ln.Close()
	if _, ok := tcpAlive(ctx, ip, []int{port}); !ok {
		t.Fatal("отказ в соединении должен считаться живым адресом")
	}
}
