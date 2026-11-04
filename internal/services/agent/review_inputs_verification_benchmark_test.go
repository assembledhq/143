package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Run with go test ./internal/services/agent -run '^$' -bench BenchmarkVerifyCapturedReviewObjects -count 5.
// Each operation includes Python startup and reads an already packed, local Git
// closure with a warm filesystem cache. Setup, checkout, and fetch are excluded.
func BenchmarkVerifyCapturedReviewObjects(b *testing.B) {
	for _, path := range []string{"/usr/bin/git", "/usr/bin/python3"} {
		if _, err := os.Stat(path); err != nil {
			b.Skipf("local verifier dependency %s is unavailable: %v", path, err)
		}
	}
	batched, _, found := strings.Cut(prepareCodeReviewInputsScript, "\ndef digest(file):")
	require.True(b, found, "benchmark must exercise the production verifier")
	prelude, _, found := strings.Cut(batched, "\ndef git(")
	require.True(b, found, "serial verifier must use the same Git environment and deadline")
	serial := prelude + serialReviewObjectVerifier
	for _, workload := range []struct {
		name       string
		smallBlobs int
		blobBytes  int
	}{
		{name: "small", smallBlobs: 2048},
		{name: "mixed", smallBlobs: 2048, blobBytes: 8 * 1024 * 1024},
		{name: "many-small", smallBlobs: 16384},
	} {
		b.Run(workload.name, func(b *testing.B) {
			dir := b.TempDir()
			env := []string{
				"PATH=/usr/bin:/bin",
				"GIT_CONFIG_NOSYSTEM=1",
				"GIT_CONFIG_GLOBAL=/dev/null",
				"GIT_DEFAULT_HASH=sha1",
				"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
				"GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
			}
			git := func(input string, args ...string) string {
				ctx, cancel := context.WithTimeout(b.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "/usr/bin/git", args...)
				cmd.Dir = dir
				cmd.Env = env
				cmd.Stdin = strings.NewReader(input)
				out, err := cmd.Output()
				if err != nil {
					b.Fatalf("fixture Git command %v failed: %v", args, err)
				}
				return strings.TrimSpace(string(out))
			}
			git("", "init", "-q", "--template=")
			git("", "config", "user.name", "Review Benchmark")
			git("", "config", "user.email", "review-benchmark@example.com")
			for i := range workload.smallBlobs {
				name := filepath.Join(dir, fmt.Sprintf("file-%04d.txt", i))
				content := fmt.Appendf(nil, "synthetic review benchmark object %04d\n", i)
				require.NoError(b, os.WriteFile(name, content, 0o600), "fixture should contain distinct small blobs")
			}
			random := rand.NewChaCha8([32]byte{})
			commit := func(name string) string {
				if workload.blobBytes > 0 {
					data := make([]byte, workload.blobBytes)
					_, err := random.Read(data)
					require.NoError(b, err, "fixture should generate deterministic large blobs")
					require.NoError(b, os.WriteFile(filepath.Join(dir, "large.bin"), data, 0o600), "fixture should write the large blob")
				}
				require.NoError(b, os.WriteFile(filepath.Join(dir, "branch.txt"), []byte(name), 0o600), "fixture should distinguish captured trees")
				git("", "add", "--all")
				git("", "commit", "-qm", name)
				return git("", "rev-parse", "HEAD")
			}
			common := commit("common")
			base := commit("base")
			git("", "checkout", "-q", "-b", "review", common)
			head := commit("head")
			mergeBase := git("", "merge-base", base, head)
			require.Equal(b, common, mergeBase, "fixture should have three distinct captured commits")
			git("", "repack", "-adq")
			oids := strings.Fields(git("", "rev-list", "--objects", "--no-object-names", "--missing=print", "--no-walk", base, head, mergeBase))
			slices.Sort(oids)
			var bytesInClosure int64
			sizes := strings.Fields(git(strings.Join(oids, "\n")+"\n", "cat-file", "--batch-check=%(objectsize)"))
			require.Len(b, sizes, len(oids), "fixture should report a size for each object")
			for _, size := range sizes {
				value, err := strconv.ParseInt(size, 10, 64)
				require.NoError(b, err, "captured object should exist and have a valid byte count")
				bytesInClosure += value
			}
			encoded, err := json.Marshal(map[string]string{"base_sha": base, "head_sha": head, "merge_base_sha": mergeBase})
			require.NoError(b, err, "fixture should encode the captured identity")
			for _, verifier := range []struct {
				name   string
				script string
			}{
				{name: "serial", script: serial},
				{name: "batched", script: batched},
			} {
				b.Run(verifier.name, func(b *testing.B) {
					run := func(script string) []byte {
						ctx, cancel := context.WithTimeout(b.Context(), 40*time.Second)
						defer cancel()
						deadline := strconv.FormatInt(time.Now().Add(30*time.Second).Unix(), 10)
						cmd := exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-S", "-c", script, string(encoded), ".git/unused", "", deadline, "true", "true", "true")
						cmd.Dir = dir
						cmd.Env = env
						out, err := cmd.Output()
						if err != nil {
							if exit, ok := err.(*exec.ExitError); ok {
								b.Fatalf("captured object verification failed: %v\n%s", err, exit.Stderr)
							}
							b.Fatalf("captured object verification failed: %v", err)
						}
						return out
					}
					verified := strings.Fields(string(run(verifier.script + benchmarkObserveReviewObjectHashes)))
					slices.Sort(verified)
					require.Equal(b, oids, verified, "verifier must rehash each object in the independent Git closure")
					script := verifier.script + "\nverify_captured_objects(identity[\"merge_base_sha\"])\n"
					b.SetBytes(bytesInClosure)
					for b.Loop() {
						if out := run(script); len(out) != 0 {
							b.Fatalf("verification unexpectedly produced output: %s", out)
						}
					}
					b.ReportMetric(float64(len(oids)), "objects/op")
					b.ReportMetric(float64(bytesInClosure), "input-bytes/op")
				})
			}
		})
	}
}

const benchmarkObserveReviewObjectHashes = `
class ObservedDigest:
    def __init__(self, digest):
        self.digest = digest

    def __getattr__(self, name):
        return getattr(self.digest, name)

    def hexdigest(self):
        oid = self.digest.hexdigest()
        print(oid)
        return oid

sha1 = hashlib.sha1
sha256 = hashlib.sha256
hashlib.sha1 = lambda *args, **kwargs: ObservedDigest(sha1(*args, **kwargs))
hashlib.sha256 = lambda *args, **kwargs: ObservedDigest(sha256(*args, **kwargs))
verify_captured_objects(identity["merge_base_sha"])
`

// Frozen ObjectReader and verifier from 26ad8f22c for the per-object exchange
// baseline. Keep its read, hashing, wait, and cleanup behavior intact.
const serialReviewObjectVerifier = `
class ObjectReader:
    def __init__(self, stream):
        self.fd = stream.fileno()
        self.buffer = bytearray()

    def receive(self, size):
        if not select.select([self.fd], [], [], remaining())[0]:
            raise TimeoutError("captured review object verification timed out")
        return os.read(self.fd, size)

    def line(self):
        while b"\n" not in self.buffer:
            if len(self.buffer) > 128:
                raise ValueError("captured review object header is invalid")
            chunk = self.receive(4096)
            if not chunk:
                if self.buffer:
                    raise ValueError("captured review object header is incomplete")
                return None
            self.buffer.extend(chunk)
        end = self.buffer.index(b"\n")
        if end > 128:
            raise ValueError("captured review object header is invalid")
        line = bytes(self.buffer[:end])
        del self.buffer[:end + 1]
        return line

    def chunk(self, size):
        if self.buffer:
            count = min(size, len(self.buffer))
            data = bytes(self.buffer[:count])
            del self.buffer[:count]
        else:
            data = self.receive(min(size, 1024 * 1024))
        if not data:
            raise ValueError("captured review object body is incomplete")
        return data

class ObjectCleanupError(RuntimeError):
    def __init__(self, failures):
        super().__init__("captured review object process cleanup could not be confirmed")
        self.failures = failures

def valid_oid(value):
    return len(value) in (40, 64) and all(byte in b"0123456789abcdef" for byte in value)

def verify_captured_objects(merge_base):
    if not isinstance(merge_base, str) or not valid_oid(merge_base.encode()):
        raise ValueError("captured review merge base is invalid")
    processes = []
    try:
        objects = subprocess.Popen(git_command("rev-list", "--objects", "--no-object-names", "--missing=print", "--no-walk", identity["base_sha"], identity["head_sha"], merge_base), stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=git_environment(), bufsize=0)
        processes.append(objects)
        batch = subprocess.Popen(git_command("cat-file", "--batch"), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, env=git_environment(), bufsize=0)
        processes.append(batch)
        os.set_blocking(batch.stdin.fileno(), False)
        names, contents = ObjectReader(objects.stdout), ObjectReader(batch.stdout)
        while (oid := names.line()) is not None:
            if not valid_oid(oid):
                raise ValueError("captured review object is missing or invalid")
            request = oid + b"\n"
            while request:
                if not select.select([], [batch.stdin.fileno()], [], remaining())[1]:
                    raise TimeoutError("captured review object verification timed out")
                request = request[os.write(batch.stdin.fileno(), request):]
            header = contents.line()
            fields = header.split() if header is not None else []
            if len(fields) != 3 or fields[0] != oid or fields[1] not in (b"commit", b"tree", b"blob") or not fields[2].isdigit():
                raise ValueError("captured review object is missing or invalid")
            size = int(fields[2])
            digest = hashlib.sha1() if len(oid) == 40 else hashlib.sha256()
            digest.update(fields[1] + b" " + fields[2] + b"\0")
            while size:
                remaining()
                data = contents.chunk(size)
                digest.update(data)
                size -= len(data)
            if contents.chunk(1) != b"\n" or digest.hexdigest().encode() != oid:
                raise ValueError("captured review source object hash mismatch")
        if objects.wait(timeout=remaining()) != 0:
            raise ValueError("captured review object closure is incomplete")
        batch.stdin.close()
        if batch.wait(timeout=remaining()) != 0:
            raise ValueError("captured review object verification failed")
    finally:
        original = sys.exc_info()[1]
        failures = []
        for process in processes:
            try:
                if process.poll() is None:
                    process.kill()
            except Exception as error:
                failures.append(error)
            try:
                process.wait(timeout=3)
            except Exception as error:
                failures.append(error)
            for stream in (process.stdin, process.stdout):
                try:
                    if stream is not None:
                        stream.close()
                except Exception as error:
                    failures.append(error)
        if failures:
            raise ObjectCleanupError(failures) from original

`
