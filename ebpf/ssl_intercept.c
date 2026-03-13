// ebpf/ssl_intercept.c — CareerScout Tier 3 eBPF Program
//
// Hooks into SSL_write and SSL_read system calls at the kernel level to
// intercept TLS plaintext BEFORE encryption occurs. This makes the
// interception completely invisible to the browser's anti-bot checks.
//
// Compilation:
//   clang -O2 -g -Wall -target bpf \
//     -I/usr/include/bpf \
//     -c ssl_intercept.c -o ssl_intercept.o
//
// Requirements:
//   - Linux kernel 5.8+ (BPF ring buffer support)
//   - Root access or CAP_BPF + CAP_SYS_ADMIN capabilities
//   - libbpf (loaded by the Go sidecar)
//
// NOTE: Tier 3 runs on dedicated AWS EC2 instances ONLY.
//       This cannot run on macOS.

#include <linux/bpf.h>
#include <linux/ptrace.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>

// Maximum payload size to capture per SSL_write call.
// Larger payloads are truncated — the Go sidecar requests the full body separately.
#define MAX_DATA_SIZE 4096

// ── Ring buffer for captured payloads ────────────────────────────────────────
// The Go sidecar reads from this buffer and classifies the captured data.
struct {
    __uint(type, BPF_MAP_TYPE_RINGBUF);
    __uint(max_entries, 256 * 1024 * 1024); // 256 MB ring buffer
} ringbuf SEC(".maps");

// ── Event structure written to the ring buffer ────────────────────────────────
struct ssl_event {
    __u32 pid;
    __u32 tid;
    __u64 timestamp_ns;
    __u32 data_len;
    __u8  is_write;       // 1 = SSL_write, 0 = SSL_read
    __u8  data[MAX_DATA_SIZE];
};

// ── uprobes: hook SSL_write and SSL_read ─────────────────────────────────────
// These attach to the OpenSSL library in the Chromium process.
// The exact library path is set by the Go sidecar via libbpf.

SEC("uprobe/SSL_write")
int BPF_UPROBE(ssl_write_entry, void *ssl, const void *buf, int num)
{
    if (num <= 0) return 0;

    struct ssl_event *event = bpf_ringbuf_reserve(&ringbuf, sizeof(struct ssl_event), 0);
    if (!event) return 0;

    event->pid          = bpf_get_current_pid_tgid() >> 32;
    event->tid          = bpf_get_current_pid_tgid() & 0xFFFFFFFF;
    event->timestamp_ns = bpf_ktime_get_ns();
    event->is_write     = 1;
    event->data_len     = (num < MAX_DATA_SIZE) ? num : MAX_DATA_SIZE;

    // Read plaintext from the SSL buffer before encryption
    long err = bpf_probe_read_user(event->data, event->data_len, buf);
    if (err < 0) {
        bpf_ringbuf_discard(event, 0);
        return 0;
    }

    bpf_ringbuf_submit(event, 0);
    return 0;
}

SEC("uprobe/SSL_read_ex")
int BPF_UPROBE(ssl_read_entry, void *ssl, void *buf, size_t num, size_t *readbytes)
{
    // We primarily care about SSL_write (outbound requests).
    // SSL_read captures responses which can be useful for schema diff detection.
    if (num <= 0) return 0;

    struct ssl_event *event = bpf_ringbuf_reserve(&ringbuf, sizeof(struct ssl_event), 0);
    if (!event) return 0;

    event->pid          = bpf_get_current_pid_tgid() >> 32;
    event->tid          = bpf_get_current_pid_tgid() & 0xFFFFFFFF;
    event->timestamp_ns = bpf_ktime_get_ns();
    event->is_write     = 0;
    event->data_len     = (num < MAX_DATA_SIZE) ? (__u32)num : MAX_DATA_SIZE;

    long err = bpf_probe_read_user(event->data, event->data_len, buf);
    if (err < 0) {
        bpf_ringbuf_discard(event, 0);
        return 0;
    }

    bpf_ringbuf_submit(event, 0);
    return 0;
}

char LICENSE[] SEC("license") = "Dual BSD/GPL";

