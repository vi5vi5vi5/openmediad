package main

import (
	"flag"
	"fmt"
	"time"

	"openmediad/internal/libs/alsactl"
)

// mixerCmd — регулятор ALSA через управляющее устройство (как его видит выход Bluetooth): значение, пределы и изменения.
func mixerCmd(args []string) error {
	fs := flag.NewFlagSet("mixer", flag.ExitOnError)
	watch := fs.Duration("watch", 0, "следить за изменениями столько времени (0 — только прочитать)")
	fs.Parse(args)
	name := "Master"
	if fs.NArg() > 0 {
		name = fs.Arg(0)
	}
	v, lo, hi, err := alsactl.ReadInteger(name)
	if err != nil {
		return err
	}
	fmt.Printf("%s = %d (пределы %d…%d)\n", name, v, lo, hi)
	if *watch <= 0 {
		return nil
	}
	w, err := alsactl.WatchInteger(name)
	if err != nil {
		return err
	}
	defer w.Close()
	end := time.After(*watch)
	for {
		select {
		case <-w.Changes():
			cur, max, _ := w.Level()
			fmt.Printf("%s %s = %d из %d\n", time.Now().Format("15:04:05.000"), name, cur, max)
		case <-end:
			return w.Err()
		}
	}
}
