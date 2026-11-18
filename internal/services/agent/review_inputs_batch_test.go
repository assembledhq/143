package agent

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCapturedObjectVerificationBatchesPreserveIntegrity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		fault     string
		sha256    bool
		smallPipe bool
		largeBody bool
		wantError string
	}{
		{name: "multiple batches and short final batch"},
		{name: "sha256 objects", sha256: true},
		{name: "minimum atomic pipe allowance", smallPipe: true},
		{name: "body larger than both pipe buffers", largeBody: true},
		{name: "transient blocked write", fault: "blocked_write"},
		{name: "zero length write fails closed", fault: "zero_write", wantError: "request was not written"},
		{name: "wrong response order", fault: "wrong_oid", wantError: "object is missing or invalid"},
		{name: "missing object", fault: "missing", wantError: "object is missing or invalid"},
		{name: "invalid object type", fault: "type", wantError: "object is missing or invalid"},
		{name: "invalid object size", fault: "size", wantError: "object is missing or invalid"},
		{name: "corrupted later object", fault: "hash", wantError: "source object hash mismatch"},
		{name: "truncated later body", fault: "truncated", wantError: "body is incomplete"},
		{name: "invalid body delimiter", fault: "delimiter", wantError: "source object hash mismatch"},
		{name: "failed closure enumeration", fault: "enumerator_exit", wantError: "closure is incomplete"},
		{name: "failed batch process", fault: "batch_exit", wantError: "object verification failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			identity, err := codeReviewInputIdentityFromSession(reviewInputsSession(t, strings.Repeat("a", 40), strings.Repeat("b", 40)))
			require.NoError(t, err, "batch fixture must retain the complete review identity")
			encoded, err := json.Marshal(identity)
			require.NoError(t, err, "batch fixture identity must encode")
			options, err := json.Marshal(map[string]any{"fault": tt.fault, "sha256": tt.sha256, "small_pipe": tt.smallPipe, "large_body": tt.largeBody, "want_error": tt.wantError})
			require.NoError(t, err, "batch fixture options must encode")
			prefix, _, found := strings.Cut(prepareCodeReviewInputsScript, "\ndef digest(file):")
			require.True(t, found, "batch regressions must exercise the production verifier")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			command := exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-S", "-c", prefix+capturedObjectBatchFixture, string(encoded), ".git/unused", "", strconv.FormatFloat(float64(deadline.UnixNano())/float64(time.Second), 'f', 6, 64), "true", "true", "true", string(options))
			output, err := command.CombinedOutput()
			require.NoError(t, err, "bounded batches must preserve framing, hashes and process cleanup: %s", output)
			require.Equal(t, "batch_verification_confirmed\n", string(output), "fixture must confirm the exact expected verifier result")
		})
	}
}

// The fake Git reader waits for a whole bounded batch before responding. A
// request/response-at-a-time verifier therefore cannot pass the success cases.
// Real pipes exercise backpressure, including an object larger than pipe memory.
const capturedObjectBatchFixture = `
options = json.loads(sys.argv[8])
pipe_limit = 512 if options["small_pipe"] else 4096
limit = min(32, pipe_limit // 65)
count = limit + 3
object_source = '''
import hashlib,json,os,sys
options = json.loads(sys.argv[1])
limit = int(sys.argv[2])
count = limit + 3
items = []
for index in range(count):
    body = (b"large captured object\\0" * (512 * 1024)) if options["large_body"] and index == 0 else ("captured-object-%d" % index).encode()
    kind = (b"blob", b"tree", b"commit")[index % 3]
    hasher = hashlib.sha256 if options["sha256"] else hashlib.sha1
    oid = hasher(kind + b" " + str(len(body)).encode() + b"\\0" + body).hexdigest().encode()
    items.append((oid,kind,body))
'''
# Interpret the embedded byte escapes once, just as a standalone Python script.
object_source = object_source.replace("\\\\0", "\\0")
enumerator = object_source + '''
os.write(1, b"\\n".join(item[0] for item in items) + b"\\n")
sys.exit(1 if options["fault"] == "enumerator_exit" else 0)
'''
reader = object_source + '''
for start in range(0,count,limit):
    group = items[start:start+limit]
    requests = [sys.stdin.buffer.readline().rstrip(b"\\n") for _ in group]
    if requests != [item[0] for item in group]:
        sys.exit(2)
    for offset,(oid,kind,body) in enumerate(group):
        index = start + offset
        fault = options["fault"] if index == limit else ""
        header_oid = b"0" * len(oid) if fault == "wrong_oid" else oid
        header_type = b"tag" if fault == "type" else kind
        size = b"-1" if fault == "size" else str(len(body)).encode()
        if fault == "missing":
            sys.stdout.buffer.write(oid + b" missing\\n")
            sys.stdout.buffer.flush()
            sys.exit(0)
        sys.stdout.buffer.write(header_oid + b" " + header_type + b" " + size + b"\\n")
        if fault == "truncated":
            sys.stdout.buffer.write(body[:-1])
            sys.stdout.buffer.flush()
            sys.exit(0)
        sys.stdout.buffer.write((b"x" + body[1:]) if fault == "hash" else body)
        sys.stdout.buffer.write(b"!" if fault == "delimiter" else b"\\n")
    sys.stdout.buffer.flush()
sys.stdin.buffer.read()
sys.exit(1 if options["fault"] == "batch_exit" else 0)
'''
enumerator = enumerator.replace("\\\\n", "\\n")
reader = reader.replace("\\\\n", "\\n")
real_popen, real_write = subprocess.Popen, os.write
created, writes = [], []
def launch(args, **kwargs):
    body = enumerator if not created else reader
    child = real_popen(["/usr/bin/python3", "-I", "-S", "-c", body, json.dumps(options), str(limit)], **kwargs)
    created.append(child)
    return child
def write(fd, request):
    assert len(request) <= pipe_limit, "request exceeded atomic pipe allowance"
    writes.append(len(request))
    if len(writes) == 1 and options["fault"] == "blocked_write":
        raise BlockingIOError("synthetic temporary pipe backpressure")
    if options["fault"] == "zero_write":
        return 0
    return real_write(fd, request)
subprocess.Popen = launch
os.write = write
os.fpathconf = lambda *_: pipe_limit
try:
    try:
        verify_captured_objects(identity["base_sha"])
    except ValueError as error:
        assert options["want_error"] and options["want_error"] in str(error), str(error)
    else:
        assert not options["want_error"], "invalid captured source was accepted"
        assert len(writes) < count, "requests were not batched"
    assert len(created) == 2 and all(child.poll() is not None for child in created), "Git children were not reaped"
    assert all(child.stdout.closed and (child.stdin is None or child.stdin.closed) for child in created), "Git pipes remained open"
    print("batch_verification_confirmed")
finally:
    for child in created:
        if child.poll() is None:
            child.kill()
        child.wait(timeout=3)
`
