# BPF Headers

This directory should contain the kernel BTF headers required to compile the eBPF programs.

## Generate vmlinux.h

Generate from your running kernel:

```bash
bpftool btf dump file /sys/kernel/btf/vmlinux format c > vmlinux.h
```

Or from a specific kernel image:

```bash
bpftool btf dump file /boot/vmlinuz-$(uname -r) format c > vmlinux.h
```

## Required Files

- `vmlinux.h` — Kernel type definitions (auto-generated, not checked in)

The `Dockerfile.agent` build stage handles this automatically using the builder image's kernel headers.
