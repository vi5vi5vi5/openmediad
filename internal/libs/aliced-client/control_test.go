package alicedclient

import (
	"context"
	"net"
	"testing"
	"time"

	"openmediad/internal/core/quasar/pb"
	"openmediad/internal/core/quasar/wire"
)

// fakeAliced — aliced на одном соединении: здоровается, как настоящий, и отдаёт присланные кадры.
func fakeAliced(t *testing.T, greet bool) (*Client, <-chan wire.Msg) {
	t.Helper()
	got := make(chan wire.Msg, 4)
	c := &Client{Dial: func(context.Context, string) (net.Conn, error) {
		cli, srv := net.Pipe()
		go func() {
			defer srv.Close()
			if greet {
				wire.WriteFrame(srv, wire.Wrap(wire.TypeRemoting, nil)) // до приветствия бывают и другие кадры
				wire.WriteFrame(srv, wire.Wrap(wire.TypeHandshake, pb.AppendString(nil, 1, "aliced")))
			}
			fr := wire.NewFrameReader(srv)
			for {
				f, err := fr.Next()
				if err != nil {
					close(got)
					return
				}
				m, _ := wire.Unwrap(append([]byte(nil), f...))
				got <- m
			}
		}()
		return cli, nil
	}}
	return c, got
}

// Как maind --cli: после приветствия — регистрация клиента, затем команда.
func TestSendRegistersThenCommands(t *testing.T) {
	for cmd, kind := range map[Command]uint64{Toggle: 2, Pause: 1, Next: 6, Prev: 7} {
		c, got := fakeAliced(t, true)
		if err := c.Send(context.Background(), cmd); err != nil {
			t.Fatalf("%v: %v", cmd, err)
		}
		reg, ctl := <-got, <-got
		if obj, _ := pb.Str(reg.Body, 7); obj != "EndpointStorage_" {
			t.Fatalf("%v: первым — регистрация, а пришло %q", cmd, obj)
		}
		if obj, _ := pb.Str(ctl.Body, 7); obj != "PlaybackControlCapability" {
			t.Fatalf("%v: объект %q", cmd, obj)
		}
		method, _ := pb.Bytes(ctl.Body, 13)
		if k, _ := pb.Uint(method, 1); k != kind {
			t.Errorf("%v: вид %d, ждали %d", cmd, k, kind)
		}
	}
}

func TestSendWithoutGreetingFails(t *testing.T) {
	c, _ := fakeAliced(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.Send(ctx, Next); err == nil {
		t.Fatal("aliced не поздоровался — ошибка, а не команда в пустоту")
	}
}

func TestNewID(t *testing.T) {
	a, b := newID(), newID()
	if len(a) != 36 || a == b || a[14] != '4' {
		t.Fatalf("%q %q", a, b)
	}
}

// Текстовый запрос — тоже после регистрации, объектом AliceCapability.
func TestAsk(t *testing.T) {
	c, got := fakeAliced(t, true)
	if err := c.Ask(context.Background(), "поставь на паузу"); err != nil {
		t.Fatal(err)
	}
	<-got
	q := <-got
	if obj, _ := pb.Str(q.Body, 7); obj != "AliceCapability" {
		t.Fatalf("объект %q", obj)
	}
}
