#!/usr/bin/env python3
"""One-shot, exact-source release image publication; default mode performs no I/O."""
import argparse
import datetime
import gzip
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import tarfile
import time

# UNBOUND placeholders prevent publication before the local binding operation.
RELEASE = TREE = ARCHIVE = RECIPE = BRANCH = IMAGE = TAG = 'UNBOUND'
WORKFLOW_PATH = '.github/workflows/release-images-once.yml'
HELPER_PATH = '.github/scripts/release-images-once.py'
BINDINGS_PATH = '.github/scripts/release-image-bindings.json'
ALLOWLIST = {
    'server': ('ghcr.io/assembledhq/143-server', 'Dockerfile'),
    'sandbox': ('ghcr.io/assembledhq/143-sandbox', 'sandbox/Dockerfile'),
    'frontend': ('ghcr.io/assembledhq/143-frontend', 'Dockerfile.frontend'),
}
RECIPE_PATH = IMAGE_KEY = 'UNBOUND'


def validate_bindings(pins):
    require(set(pins) == {'schema', 'release', 'tree', 'archive_sha256', 'recipes'}, 'binding fields differ')
    require(pins['schema'] == 1, 'binding schema differs')
    for key in ('release', 'tree'):
        require(isinstance(pins[key], str) and re.fullmatch(r'[0-9a-f]{40}', pins[key]), 'unbound source identity')
    require(isinstance(pins['archive_sha256'], str) and re.fullmatch(r'[0-9a-f]{64}', pins['archive_sha256']), 'unbound archive')
    require(set(pins['recipes']) == {item[1] for item in ALLOWLIST.values()}, 'recipe allowlist differs')
    require(all(isinstance(value, str) and re.fullmatch(r'[0-9a-f]{64}', value)
                for value in pins['recipes'].values()), 'unbound recipe')
    return pins


def configure(pins, image):
    global RELEASE, TREE, ARCHIVE, RECIPE, BRANCH, IMAGE, TAG, RECIPE_PATH, IMAGE_KEY
    validate_bindings(pins)
    require(image in ALLOWLIST, 'image is outside fixed allowlist')
    IMAGE_KEY = image
    IMAGE, RECIPE_PATH = ALLOWLIST[image]
    RELEASE, TREE, ARCHIVE = pins['release'], pins['tree'], pins['archive_sha256']
    RECIPE = pins['recipes'][RECIPE_PATH]
    BRANCH = 'refs/heads/codex/build-rollout-artifacts-' + RELEASE
    TAG = IMAGE + ':' + RELEASE


DIGEST = re.compile(r'^sha256:[0-9a-f]{64}$')


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def sha_file(path):
    digest = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def command(args, *, cwd=None, timeout=60):
    # Git status may refresh its index unless optional locks are disabled.
    env = dict(os.environ, GIT_OPTIONAL_LOCKS='0') if args[0] == 'git' else None
    return subprocess.run(args, cwd=cwd, timeout=timeout, check=True,
                          capture_output=True, text=True, env=env).stdout.strip()


def write_new(path, value):
    with Path(path).open('x', encoding='utf-8') as stream:
        json.dump(value, stream, sort_keys=True, indent=2)
        stream.write('\n')


def context(env):
    require(env.get('GITHUB_ACTIONS') == 'true', 'hosted Actions context required')
    require(env.get('GITHUB_REPOSITORY') == 'assembledhq/143', 'repository differs')
    require(env.get('GITHUB_EVENT_NAME') == 'push', 'only branch push is allowed')
    require(env.get('GITHUB_REF') == BRANCH, 'branch differs')
    require(env.get('GITHUB_RUN_ATTEMPT') == '1', 'reruns are refused; reconcile prior publication first')
    commit = env.get('GITHUB_SHA', '')
    require(re.fullmatch(r'[0-9a-f]{40}', commit) is not None, 'workflow commit absent')
    require(env.get('GITHUB_WORKFLOW_SHA') == commit, 'workflow source differs from push commit')
    require(env.get('GITHUB_WORKFLOW_REF') == 'assembledhq/143/' + WORKFLOW_PATH + '@' + BRANCH,
            'workflow identity differs')
    require(env.get('GITHUB_RUN_ID', '').isdigit(), 'run identity absent')
    return {'workflow_commit': commit, 'workflow_ref': env['GITHUB_WORKFLOW_REF'],
            'run_id': env['GITHUB_RUN_ID'], 'run_attempt': 1,
            'job': env.get('GITHUB_JOB', ''), 'repository': 'assembledhq/143'}


def verify_source(source, helper_source, out, identity):
    require(command(['git', 'rev-parse', 'HEAD'], cwd=source) == RELEASE, 'release checkout differs')
    require(command(['git', 'rev-parse', 'HEAD^{tree}'], cwd=source) == TREE, 'release tree differs')
    require(not command(['git', 'status', '--porcelain', '--untracked-files=all'], cwd=source),
            'release checkout is dirty')
    require(not command(['git', 'ls-files', '--others', '--ignored', '--exclude-standard'], cwd=source),
            'ignored files in release context are forbidden')
    require(command(['git', 'rev-parse', 'HEAD'], cwd=helper_source) == identity['workflow_commit'],
            'helper checkout differs from workflow commit')
    require(not command(['git', 'status', '--porcelain', '--untracked-files=all'], cwd=helper_source),
            'workflow checkout is dirty')
    require(not command(['git', 'ls-files', '--others', '--ignored', '--exclude-standard'], cwd=helper_source),
            'ignored files in workflow checkout are forbidden')
    archive = out / 'release.tar'
    command(['git', 'archive', '--format=tar', '--output=' + str(archive), RELEASE], cwd=source)
    require(sha_file(archive) == ARCHIVE, 'release archive differs')
    require(sha_file(source / RECIPE_PATH) == RECIPE, 'exact recipe differs')
    require(not any(key in os.environ for key in ('NODE_OPTIONS', 'CIRCLE_NODE_TOTAL', 'ONEFORTYTHREE_ENV')),
            'unexpected build environment override')
    return dict(identity, release=RELEASE, tree=TREE, source_archive_sha256=ARCHIVE,
                dockerfile_sha256=RECIPE, recipe_overlay=None,
                helper_sha256=sha_file(helper_source / HELPER_PATH),
                workflow_sha256=sha_file(helper_source / WORKFLOW_PATH),
                bindings_sha256=sha_file(helper_source / BINDINGS_PATH), image_key=IMAGE_KEY, recipe_path=RECIPE_PATH)


def require_absent(inspect_result):
    """Only a registry not-found result permits publication; auth/network errors fail shut."""
    require(inspect_result.returncode != 0, 'exact SHA tag already exists; reconcile, never overwrite')
    error = inspect_result.stderr.lower()
    missing = ('manifest unknown' in error or TAG.lower() + ': not found' in error)
    # A release digest can contain digits such as 503 or 429; it is not an HTTP status.
    diagnostic = error.replace(TAG.lower(), '[exact-image-tag]').replace(RELEASE.lower(), '[release]')
    unsafe = any(text in diagnostic for text in ('unauthorized', 'denied', 'forbidden', 'timeout',
                                                 'connection', 'no such host', 'tls'))
    status = r'(?:429|500|502|503)'
    http_failure = (re.search(r'(?<![0-9a-z])' + status + r'(?![0-9a-z])', diagnostic) or
                    re.search(r'http(?:/\d+(?:\.\d+)?)?\s*[:=]?\s*' + status + r'(?![0-9])', diagnostic))
    require(missing and not unsafe and not http_failure, 'tag absence is unproven; no publication allowed')


def check_tag_absent():
    result = subprocess.run(['docker', 'buildx', 'imagetools', 'inspect', TAG],
                            capture_output=True, text=True, timeout=45)
    require_absent(result)


def vertex_updates(record):
    stack = [record]
    while stack:
        item = stack.pop()
        if isinstance(item, list):
            stack.extend(item)
        elif isinstance(item, dict):
            yield item
            stack.extend(value for value in item.values() if isinstance(value, (dict, list)))


class CacheProof:
    def __init__(self):
        self.vertices = {}

    def consume(self, line):
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            return
        for item in vertex_updates(value):
            identity = item.get('id') or item.get('digest')
            name = item.get('name', '')
            if identity and (identity in self.vertices or
                             isinstance(name, str) and re.search(r'(?:^|\])\s*RUN\s', name)):
                vertex = self.vertices.setdefault(identity, {'seen': time.monotonic()})
                vertex.update(item)

    def check_running(self):
        for vertex in self.vertices.values():
            require(not (vertex.get('started') and not vertex.get('cached') and
                         (vertex.get('completed') or time.monotonic() - vertex['seen'] > 3)),
                    'unexpected source RUN during cached export/publication; stop and reconcile')

    def finish(self):
        require(bool(self.vertices), 'no positive RUN cache evidence')
        require(all(vertex.get('cached') is True for vertex in self.vertices.values()),
                'not every source RUN was positively cached')
        return {'source_run_vertices': len(self.vertices), 'all_source_runs_cached': True}


def build(args, log, *, cached=False, timeout=1500):
    proof = CacheProof()
    with log.open('xb') as output:
        proc = subprocess.Popen(args, stdout=output, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            deadline = time.monotonic() + timeout
            with log.open('rb') as progress:
                pending = b''
                while True:
                    pending += progress.read()
                    lines = pending.split(b'\n')
                    pending = lines.pop()
                    for line in lines:
                        if cached:
                            proof.consume(line)
                    if cached:
                        proof.check_running()
                    code = proc.poll()
                    if code is not None:
                        # Read bytes written between the first read and process completion.
                        pending += progress.read()
                        for line in pending.splitlines():
                            if cached:
                                proof.consume(line)
                        if code != 0:
                            raise subprocess.CalledProcessError(code, args)
                        break
                    require(time.monotonic() < deadline, 'bounded build deadline exceeded')
                    time.sleep(0.2)
            return proof.finish() if cached else {'cache_only_completed': True}
        finally:
            if proc.poll() is None:
                os.killpg(proc.pid, signal.SIGTERM)
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(proc.pid, signal.SIGKILL)
                    proc.wait(timeout=10)


MAX_LAYER_UNCOMPRESSED_BYTES = 64 * 1024**3
MAX_TAR_ENTRIES = 1_000_000
MAX_TAR_METADATA_BYTES = 64 * 1024
ALLOCATION_UNIT = 4096
METADATA_BYTES_PER_INODE = 8192
METADATA_BYTES_PER_LAYER = 1024**2
MAX_BUDGET_BYTES = (1 << 63) - 1


class BoundedLayerReader:
    def __init__(self, stream):
        self.stream, self.count = stream, 0

    def read(self, size):
        require(0 <= size <= 1024**2, 'unbounded layer read refused')
        data = self.stream.read(min(size, MAX_LAYER_UNCOMPRESSED_BYTES - self.count + 1))
        self.count += len(data)
        require(self.count <= MAX_LAYER_UNCOMPRESSED_BYTES, 'expanded layer byte limit exceeded')
        return data


class BudgetTarInfo(tarfile.TarInfo):
    @classmethod
    def fromtarfile(cls, archive, *args, **kwargs):
        try:
            return super().fromtarfile(archive, *args, **kwargs)
        except tarfile.EOFHeaderError:
            raise
        except tarfile.HeaderError:
            raise RuntimeError('invalid or truncated layer tar header') from None

    def _reject_sparse(self, *args):
        raise RuntimeError('sparse tar layer format is unsupported')

    _proc_sparse = _proc_gnusparse_00 = _proc_gnusparse_01 = _proc_gnusparse_10 = _reject_sparse

    def _proc_member(self, archive):
        archive._budget_headers = getattr(archive, '_budget_headers', 0) + 1
        require(archive._budget_headers <= MAX_TAR_ENTRIES, 'layer tar entry limit exceeded')
        require(0 <= self.size <= MAX_LAYER_UNCOMPRESSED_BYTES, 'layer member size limit exceeded')
        metadata = self.type in (tarfile.XHDTYPE, tarfile.XGLTYPE, tarfile.SOLARIS_XHDTYPE,
                                tarfile.GNUTYPE_LONGNAME, tarfile.GNUTYPE_LONGLINK)
        require(not metadata or self.size <= MAX_TAR_METADATA_BYTES, 'layer tar metadata limit exceeded')
        depth = getattr(archive, '_budget_depth', 0)
        require(depth < 8, 'layer tar metadata nesting limit exceeded')
        archive._budget_depth = depth + 1
        try:
            result = super()._proc_member(archive)
        finally:
            archive._budget_depth = depth
        require(result.sparse is None and not any(k.startswith('GNU.sparse') for k in result.pax_headers),
                'sparse tar metadata is unsupported')
        require(0 <= result.size <= MAX_LAYER_UNCOMPRESSED_BYTES, 'expanded member size limit exceeded')
        require(sum(len(k) + len(v) for k, v in archive.pax_headers.items()) <= MAX_TAR_METADATA_BYTES,
                'global tar metadata limit exceeded')
        return result


def layer_unpack_budget(stream, media_type, compressed_bytes):
    """Bounded estimate from verified layer bytes, without extracting or emitting paths."""
    require(isinstance(compressed_bytes, int) and 0 < compressed_bytes <= MAX_LAYER_UNCOMPRESSED_BYTES,
            'compressed layer byte limit exceeded')
    gzip_types = ('application/vnd.oci.image.layer.v1.tar+gzip',
                  'application/vnd.docker.image.rootfs.diff.tar.gzip')
    require(media_type in gzip_types + ('application/vnd.oci.image.layer.v1.tar',),
            'unsupported layer compression or media type')
    decoded = gzip.GzipFile(fileobj=stream, mode='rb') if media_type in gzip_types else stream
    regular_bytes = allocated_bytes = implicit_parents = pax_allocated_bytes = 0
    try:
        with tarfile.open(fileobj=BoundedLayerReader(decoded), mode='r|', tarinfo=BudgetTarInfo) as layer:
            while (member := layer.next()) is not None:
                parts = [part for part in member.name.split('/') if part not in ('', '.')]
                require(not member.name.startswith('/') and '..' not in parts and '\x00' not in member.name and
                        len(member.name.encode('utf-8', 'surrogateescape')) <= 4096 and bool(parts or member.isdir()),
                        'invalid layer member path')
                require('\x00' not in member.linkname and len(member.linkname.encode('utf-8', 'surrogateescape')) <= 4096,
                        'unsupported layer link target length')
                require(member.isreg() or member.isdir() or member.issym() or member.islnk() or member.isdev(),
                        'unsupported layer tar member type')
                implicit_parents += max(0, len(parts) - 1)
                pax_bytes = sum(len(k.encode('utf-8', 'surrogateescape')) + len(v.encode('utf-8', 'surrogateescape'))
                                for k, v in member.pax_headers.items())
                pax_allocated_bytes += ((pax_bytes + ALLOCATION_UNIT - 1) // ALLOCATION_UNIT) * ALLOCATION_UNIT
                if member.isreg():
                    regular_bytes += member.size
                    allocated_bytes += ((member.size + ALLOCATION_UNIT - 1) // ALLOCATION_UNIT) * ALLOCATION_UNIT
                require(regular_bytes <= MAX_LAYER_UNCOMPRESSED_BYTES and allocated_bytes <= MAX_BUDGET_BYTES,
                        'layer allocation budget overflow')
                layer.members.clear()  # Older Python versions retain streamed TarInfo objects.
            entries = getattr(layer, '_budget_headers', 0)
            while (padding := layer.fileobj.read(64 * 1024)):
                require(not padding.strip(b'\x00'), 'unexpected trailing layer data')
    finally:
        if decoded is not stream:
            decoded.close()
    inodes = entries + implicit_parents  # Duplicate parents/entries deliberately overcount each layer.
    metadata_bytes = inodes * METADATA_BYTES_PER_INODE + METADATA_BYTES_PER_LAYER + pax_allocated_bytes
    estimate = compressed_bytes + allocated_bytes + metadata_bytes
    require(estimate <= MAX_BUDGET_BYTES, 'combined layer budget overflow')
    return {'compressed_blob_bytes': compressed_bytes, 'regular_file_uncompressed_bytes': regular_bytes,
            'regular_file_allocation_bytes_4096': allocated_bytes, 'tar_entry_count': entries,
            'implicit_parent_directory_inode_upper_bound': implicit_parents, 'inode_upper_bound': inodes,
            'pax_metadata_allocation_bytes_4096': pax_allocated_bytes,
            'metadata_allowance_bytes': metadata_bytes, 'estimated_download_and_unpacked_bytes': estimate,
            'metadata_bytes_per_inode': METADATA_BYTES_PER_INODE, 'metadata_bytes_per_layer': METADATA_BYTES_PER_LAYER,
            'scope': 'local_overlay_estimate_not_physical_proof'}


def oci_receipt(archive):
    """Hash every referenced blob without extracting files or retaining image bodies."""
    with tarfile.open(archive, 'r') as tar:
        members = tar.getmembers()
        names = [member.name for member in members]
        require(len(names) == len(set(names)), 'duplicate OCI member')
        require(all(not name.startswith('/') and '..' not in Path(name).parts for name in names),
                'unsafe OCI member')
        require(all(member.isfile() or member.isdir() for member in members), 'nonregular OCI member')

        def descriptor_blob(descriptor, parse=False):
            digest = descriptor.get('digest', '')
            require(DIGEST.fullmatch(digest) is not None, 'unsupported OCI digest')
            member = tar.getmember('blobs/sha256/' + digest.split(':')[1])
            require(member.size == descriptor['size'], 'OCI descriptor size mismatch')
            require(not parse or member.size <= 4 * 1024 * 1024, 'OCI metadata too large')
            stream = tar.extractfile(member)
            hasher, chunks = hashlib.sha256(), []
            for block in iter(lambda: stream.read(1024 * 1024), b''):
                hasher.update(block)
                if parse:
                    chunks.append(block)
            require('sha256:' + hasher.hexdigest() == digest, 'OCI blob digest mismatch')
            return json.loads(b''.join(chunks)) if parse else None

        index_member = tar.getmember('index.json')
        require(index_member.size <= 4 * 1024 * 1024, 'OCI index too large')
        index = json.load(tar.extractfile(index_member))
        candidates = []

        def visit(index, depth=0):
            require(depth <= 3, 'OCI nesting too deep')
            require(len(index['manifests']) <= 8, 'OCI index too broad')
            for descriptor in index['manifests']:
                manifest = descriptor_blob(descriptor, True)
                if 'manifests' in manifest:
                    visit(manifest, depth + 1)
                else:
                    config = descriptor_blob(manifest['config'], True)
                    require(config.get('os') == 'linux' and config.get('architecture') == 'amd64',
                            'unexpected OCI platform/attestation')
                    require(config.get('config', {}).get('Labels', {}).get('org.opencontainers.image.revision') == RELEASE,
                            'OCI release revision differs')
                    budgets = []
                    for layer in manifest['layers']:
                        descriptor_blob(layer)
                        with tar.extractfile('blobs/sha256/' + layer['digest'].split(':')[1]) as stream:
                            budgets.append(layer_unpack_budget(stream, layer.get('mediaType'), layer['size']))
                    candidates.append((descriptor, manifest, budgets))
        visit(index)
        require(len(candidates) == 1, 'expected exactly one genuine Linux/amd64 image')
        descriptor, manifest, budgets = candidates[0]
        return {'manifest_digest': descriptor['digest'], 'config_digest': manifest['config']['digest'],
                'platform': 'linux/amd64', 'layers': [dict(digest=x['digest'], bytes=x['size']) for x in manifest['layers']],
                'oci_archive_sha256': sha_file(archive), 'oci_archive_bytes': archive.stat().st_size,
                'revision_verified': True, 'all_referenced_blob_digests_verified': True,
                'unpack_budget_by_layer': budgets}


def verify_registry(expected, push_metadata=None):
    manifest = json.loads(command(['docker', 'buildx', 'imagetools', 'inspect', '--format',
                                   '{{json .Manifest}}', TAG]))
    require(manifest.get('digest') == expected['manifest_digest'], 'registry manifest differs from fresh OCI')
    # provenance=false + one platform must produce that same single manifest, not a new index.
    require('manifests' not in manifest, 'unexpected published index')
    raw_manifest = json.loads(command(['docker', 'buildx', 'imagetools', 'inspect', '--raw',
                                       IMAGE + '@' + expected['manifest_digest']]))
    require(raw_manifest.get('config', {}).get('digest') == expected['config_digest'], 'registry config digest differs')
    require([dict(digest=x['digest'], bytes=x['size']) for x in raw_manifest.get('layers', [])] == expected['layers'],
            'registry layers differ from fresh OCI')
    if push_metadata is not None:
        require(push_metadata.get('containerimage.digest') == expected['manifest_digest'], 'push metadata differs')
        require(push_metadata.get('containerimage.config.digest') == expected['config_digest'], 'push config metadata differs')
    config = json.loads(command(['docker', 'buildx', 'imagetools', 'inspect', '--format',
                                '{{json .Image}}', IMAGE + '@' + expected['manifest_digest']]))
    require(config.get('os') == 'linux' and config.get('architecture') == 'amd64', 'registry platform differs')
    require(config.get('config', {}).get('Labels', {}).get('org.opencontainers.image.revision') == RELEASE,
            'registry revision differs')
    return {'tag': TAG, 'manifest_digest': expected['manifest_digest'],
            'config_digest': expected['config_digest'], 'revision_verified': True,
            'fresh_oci_and_registry_match': True}


def execute(args):
    identity = context(os.environ)
    source, helper_source, out = (Path(value).resolve() for value in (args.source, args.helper_source, args.output))
    require(not out.exists(), 'attempt output already exists; no retry')
    out.mkdir(parents=True)
    try:
        provenance = verify_source(source, helper_source, out, identity)
        write_new(out / 'source-proof.json', provenance)
        check_tag_absent()
        builder = args.builder
        require(re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9_.-]{0,100}', builder) is not None, 'builder identity invalid')
        common = ['docker', 'buildx', 'build', '--builder', builder, '--platform', 'linux/amd64',
                  '--file', str(source / RECIPE_PATH), '--build-arg', 'BUILD_SHA=' + RELEASE,
                  '--label', 'org.opencontainers.image.revision=' + RELEASE, '--tag', TAG,
                  '--provenance=false', '--progress', 'rawjson']
        def phase(name, output, cached):
            write_new(out / (name + '-intent.json'), {'phase': name, 'source': RELEASE, 'no_retry': True})
            metadata = out / (name + '-metadata.json')
            result = build(common + ['--metadata-file', str(metadata), '--output', output, str(source)],
                           out / (name + '.ndjson'), cached=cached)
            require(json.loads(metadata.read_text()).get('buildx.build.ref'), 'positive build reference absent')
            write_new(out / (name + '-result.json'), result)
            print(json.dumps({'phase': name, 'completed': True}), flush=True)
            return result
        phase('cache', 'type=cacheonly', False)
        archive = out / (IMAGE_KEY + '.oci.tar')
        export_cache = phase('oci', 'type=oci,oci-mediatypes=true,dest=' + str(archive), True)
        expected = oci_receipt(archive)
        write_new(out / 'oci-proof.json', dict(expected, cache=export_cache))
        # Source and final absence are checked immediately before the sole publication call.
        require(not command(['git', 'status', '--porcelain', '--untracked-files=all'], cwd=source), 'source changed during build')
        require(not command(['git', 'ls-files', '--others', '--ignored', '--exclude-standard'], cwd=source),
                'ignored files appeared during build')
        check_tag_absent()
        # This is genuine pre-publication evidence, never a success receipt.
        write_new(out / 'public-intent.json', {'schema': 1, 'result': 'publication_unresolved',
                  'source': provenance, 'image_key': IMAGE_KEY, 'tag': TAG, 'oci': expected,
                  'export_cache': export_cache, 'deployment_performed': False})
        pushed_cache = phase('push', 'type=image,push=true,oci-mediatypes=true', True)
        registry = verify_registry(expected, json.loads((out / 'push-metadata.json').read_text()))
        # Only this allowlisted compact receipt is eligible for public artifact upload.
        receipt = {'schema': 1, 'observed_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                   'source': provenance, 'oci': expected, 'registry': registry,
                   'export_cache': export_cache, 'push_cache': pushed_cache,
                   'publication_attempts': 1, 'deployment_performed': False,
                   'latest_or_shared_cache_written': False}
        write_new(out / 'public-receipt.json', receipt)
        print(json.dumps(receipt, sort_keys=True))
    except Exception as error:
        phases = [phase for phase in ('cache', 'oci', 'push') if (out / (phase + '-intent.json')).exists()]
        phase = phases[-1] if phases else 'source_or_admission'
        log = out / (phase + '.ndjson')
        markers = []
        if log.is_file():
            with log.open('rb') as stream:
                stream.seek(max(0, log.stat().st_size - 256 * 1024))
                tail = stream.read().lower()
            markers = [name for name, needle in (
                ('spawn_enomem', b'spawn /usr/local/bin/node enomem'),
                ('memory_allocation_failure', b'enomem'),
                ('javascript_heap_limit', b'heap out of memory'),
                ('typescript_error', b'type error:'),
                ('next_build_failed', b'next.js build worker exited'),
                ('registry_unauthorized', b'unauthorized'),
                ('registry_denied', b'denied')) if needle in tail]
        failure = {'schema': 1, 'result': 'failed_do_not_retry', 'phase': phase,
                   'workflow': identity, 'expected_release': RELEASE, 'image_key': IMAGE_KEY,
                   'source_proof_written': (out / 'source-proof.json').is_file(),
                   'error_class': type(error).__name__,
                   'exit_code': error.returncode if isinstance(error, subprocess.CalledProcessError) else None,
                   'known_log_markers': markers,
                   'log_sha256': sha_file(log) if log.is_file() else None,
                   'log_bytes': log.stat().st_size if log.is_file() else None,
                   'publication_may_have_started': 'push' in phases,
                   'deployment_performed': False,
                   'raw_log_or_environment_included': False}
        write_new(out / 'public-failure.json', failure)
        print(json.dumps(failure, sort_keys=True), flush=True)
        raise RuntimeError('one-shot image artifact failed; inspect compact failure and reconcile before any new attempt') from None


def reconcile(args):
    """Positive registry reads only. Never builds, pushes, or manufactures success."""
    intent = json.loads(Path(args.intent).read_text())
    require(intent.get('schema') == 1 and intent.get('result') == 'publication_unresolved', 'intent type differs')
    require(intent.get('image_key') == IMAGE_KEY and intent.get('tag') == TAG, 'intent image differs')
    source = intent.get('source', {})
    require(source.get('release') == RELEASE and source.get('tree') == TREE and
            source.get('source_archive_sha256') == ARCHIVE and source.get('dockerfile_sha256') == RECIPE,
            'intent source differs')
    expected = intent.get('oci', {})
    require(expected.get('all_referenced_blob_digests_verified') is True and
            expected.get('revision_verified') is True and expected.get('platform') == 'linux/amd64',
            'original genuine OCI proof absent')
    registry = verify_registry(expected)
    receipt = {'schema': 1, 'result': 'positive_registry_reconciliation', 'original_job_success': False,
               'intent_sha256': sha_file(args.intent), 'source': source, 'image_key': IMAGE_KEY,
               'oci': expected, 'registry': registry, 'publication_performed': False,
               'deployment_performed': False}
    # The owning release procedure must authenticate the intent's original run/artifact.
    write_new(Path(args.output), receipt)
    print(json.dumps(receipt, sort_keys=True))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--execute', action='store_true')
    mode.add_argument('--reconcile', action='store_true')
    parser.add_argument('--image', choices=sorted(ALLOWLIST))
    parser.add_argument('--bindings', default=str(Path(__file__).with_name('release-image-bindings.json')))
    parser.add_argument('--source')
    parser.add_argument('--helper-source')
    parser.add_argument('--output')
    parser.add_argument('--builder')
    parser.add_argument('--intent')
    args = parser.parse_args()
    if not args.execute and not args.reconcile:
        print(json.dumps({'mode': 'plan_only', 'release': 'UNBOUND_UNTIL_VALIDATED',
                          'images': sorted(ALLOWLIST), 'remote_actions': False}))
        return
    require(args.image, 'fixed image choice required')
    configure(json.loads(Path(args.bindings).read_text()), args.image)
    if args.reconcile:
        require(args.intent and args.output, 'original intent and new receipt path required')
        reconcile(args)
        return
    require(all((args.source, args.helper_source, args.output, args.builder)), 'explicit paths and owned builder required')
    require(Path(args.bindings).resolve() == (Path(args.helper_source) / BINDINGS_PATH).resolve(),
            'bindings must come from exact workflow checkout')
    execute(args)


if __name__ == '__main__':
    main()
