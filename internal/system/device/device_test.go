package device

import (
	"net"
	"testing"
	"time"

	"openmediad/internal/quasar/pb"
	"openmediad/internal/quasar/wire"
)

func TestIdentifyReadsHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		dev := pb.AppendString(nil, 1, "aliced")
		dev = pb.AppendString(dev, 2, KnownFirmware[0])
		dev = pb.AppendString(dev, 11, "bergamot")
		hs := pb.AppendBytes(nil, 1, pb.AppendBytes(nil, 1, dev))
		wire.WriteFrame(c, wire.Wrap(wire.TypeAliceState, []byte{8, 0})) // чужой кадр раньше приветствия
		wire.WriteFrame(c, wire.Wrap(wire.TypeHandshake, hs))
		time.Sleep(time.Second)
	}()
	old := AlicedAddr
	AlicedAddr = ln.Addr().String()
	defer func() { AlicedAddr = old }()
	info, err := Identify(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !info.KnownModel() || !info.KnownFirmware() {
		t.Fatalf("%+v", info)
	}
	if (Info{Model: "bergamot", Firmware: "0.1"}).KnownFirmware() {
		t.Fatal("незнакомая прошивка признана знакомой")
	}
}

func TestIdentifyWithoutAliced(t *testing.T) {
	old := AlicedAddr
	AlicedAddr = "127.0.0.1:1"
	defer func() { AlicedAddr = old }()
	if _, err := Identify(200 * time.Millisecond); err == nil {
		t.Fatal("без aliced ждали ошибку")
	}
}
