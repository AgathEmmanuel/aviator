/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package ebpf

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/go-logr/logr"
)

// Programs holds the loaded eBPF programs and maps.
type Programs struct {
	SendProbe link.Link
	RecvProbe link.Link
	Reader    *ringbuf.Reader
}

// Loader manages the lifecycle of eBPF programs.
type Loader struct {
	log       logr.Logger
	collector *Collector
	programs  *Programs
}

// NewLoader creates a new eBPF program loader.
func NewLoader(log logr.Logger, collector *Collector) *Loader {
	return &Loader{
		log:       log.WithName("ebpf-loader"),
		collector: collector,
	}
}

// Load compiles and attaches the eBPF programs to kernel hooks.
// The bpfObj parameter should be the path to the compiled .o file,
// or an embedded byte slice from go:embed.
func (l *Loader) Load(bpfObjPath string) error {
	l.log.Info("loading eBPF programs", "path", bpfObjPath)

	// Open the compiled eBPF ELF object.
	f, err := os.Open(bpfObjPath)
	if err != nil {
		return fmt.Errorf("opening eBPF object: %w", err)
	}
	defer f.Close()

	spec, err := ebpf.LoadCollectionSpecFromReader(f)
	if err != nil {
		return fmt.Errorf("loading eBPF spec: %w", err)
	}

	var coll *ebpf.Collection
	coll, err = ebpf.NewCollection(spec)
	if err != nil {
		return fmt.Errorf("creating eBPF collection: %w", err)
	}

	// Attach kprobe for tcp_sendmsg.
	sendProbe, err := link.Kprobe("tcp_sendmsg", coll.Programs["kprobe_tcp_sendmsg"], nil)
	if err != nil {
		coll.Close()
		return fmt.Errorf("attaching tcp_sendmsg kprobe: %w", err)
	}

	// Attach kprobe for tcp_rcv_established.
	recvProbe, err := link.Kprobe("tcp_rcv_established", coll.Programs["kprobe_tcp_rcv_established"], nil)
	if err != nil {
		sendProbe.Close()
		coll.Close()
		return fmt.Errorf("attaching tcp_rcv_established kprobe: %w", err)
	}

	// Open ring buffer reader.
	reader, err := ringbuf.NewReader(coll.Maps["latency_events"])
	if err != nil {
		recvProbe.Close()
		sendProbe.Close()
		coll.Close()
		return fmt.Errorf("creating ring buffer reader: %w", err)
	}

	l.programs = &Programs{
		SendProbe: sendProbe,
		RecvProbe: recvProbe,
		Reader:    reader,
	}

	l.log.Info("eBPF programs loaded and attached successfully")
	return nil
}

// Run starts reading events from the ring buffer and feeding them to the collector.
// Blocks until the context is cancelled.
func (l *Loader) Run(ctx context.Context) error {
	if l.programs == nil {
		return fmt.Errorf("eBPF programs not loaded")
	}

	l.log.Info("starting eBPF event reader")

	go func() {
		<-ctx.Done()
		l.programs.Reader.Close()
	}()

	for {
		record, err := l.programs.Reader.Read()
		if err != nil {
			if ctx.Err() != nil {
				return nil // Context cancelled, normal shutdown.
			}
			l.log.Error(err, "reading from ring buffer")
			time.Sleep(100 * time.Millisecond)
			continue
		}

		evt, err := ParseLatencyEvent(record.RawSample)
		if err != nil {
			l.log.V(2).Info("failed to parse event", "error", err)
			continue
		}

		l.collector.RecordEvent(evt)
	}
}

// Close detaches eBPF programs and releases resources.
func (l *Loader) Close() error {
	if l.programs == nil {
		return nil
	}

	l.log.Info("closing eBPF programs")

	var errs []error
	if l.programs.Reader != nil {
		if err := l.programs.Reader.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if l.programs.SendProbe != nil {
		if err := l.programs.SendProbe.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if l.programs.RecvProbe != nil {
		if err := l.programs.RecvProbe.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors closing eBPF programs: %v", errs)
	}
	return nil
}
