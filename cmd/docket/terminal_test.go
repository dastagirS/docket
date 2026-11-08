package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestTerminalInputNonInteractiveDescriptors(t *testing.T) {
	regular, err := os.Create(filepath.Join(t.TempDir(), "input"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := regular.Close(); err != nil {
			t.Error(err)
		}
	})
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, file := range []*os.File{reader, writer} {
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	descriptors, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	socket := os.NewFile(uintptr(descriptors[0]), "stdin-socket")
	peer := os.NewFile(uintptr(descriptors[1]), "stdin-peer")
	t.Cleanup(func() {
		for _, file := range []*os.File{socket, peer} {
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	information, err := socket.Stat()
	if err != nil || information.Mode()&os.ModeSocket == 0 {
		t.Fatalf("Fixture must reproduce Node's socket-backed stdin: %v", err)
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := null.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, fixture := range []struct {
		name string
		file *os.File
	}{{"regular", regular}, {"pipe", reader}, {"socket", socket}, {"null", null}} {
		t.Run(fixture.name, func(t *testing.T) {
			interactive, err := terminalInput(fixture.file)
			if err != nil || interactive {
				t.Fatalf("Noninteractive descriptor misclassified: interactive=%t error=%v", interactive, err)
			}
		})
	}
}

func TestTerminalInputClosedDescriptor(t *testing.T) {
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if interactive, err := terminalInput(file); err == nil || interactive {
		t.Fatal("A closed descriptor must remain an inspection error")
	}
}
