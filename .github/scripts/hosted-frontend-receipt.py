#!/usr/bin/env python3
"""One-shot, exact-source frontend publication; default mode performs no I/O."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import tarfile
import time

RELEASE = 'f40f2a31b1eae5e3e6b846779c5a66f266b5ec38'
TREE = '321b82d05c17b9f9b7742b685ddb3b80659a9ce1'
ARCHIVE = 'd3d1d22bad4a0677264ff21a70e807d608200466d06c03f56f003b8ccd1d5563'
RECIPE = '23b130f0d3d2ee8536e3f38d42b5e978244263e21b361ec4a50daacd851dc3ce'
BRANCH = 'refs/heads/codex/build-quota-rollout-frontend'
WORKFLOW_PATH = '.github/workflows/hosted-frontend-build-once.yml'
IMAGE = 'ghcr.io/assembledhq/143-frontend'
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
    return subprocess.run(args, cwd=cwd, timeout=timeout, check=True,
                          capture_output=True, text=True).stdout.strip()


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
    require(command(['git', 'rev-parse', 'HEAD'], cwd=helper_source) == identity['workflow_commit'],
            'helper checkout differs from workflow commit')
    require(not command(['git', 'status', '--porcelain', '--untracked-files=all'], cwd=helper_source),
            'workflow checkout is dirty')
    archive = out / 'release.tar'
    command(['git', 'archive', '--format=tar', '--output=' + str(archive), RELEASE], cwd=source)
    require(sha_file(archive) == ARCHIVE, 'release archive differs')
    require(sha_file(source / 'Dockerfile.frontend') == RECIPE, 'original frontend recipe differs')
    require(not any(key in os.environ for key in ('NODE_OPTIONS', 'CIRCLE_NODE_TOTAL', 'ONEFORTYTHREE_ENV')),
            'unexpected build environment override')
    return dict(identity, release=RELEASE, tree=TREE, source_archive_sha256=ARCHIVE,
                dockerfile_sha256=RECIPE, recipe_overlay=None,
                helper_sha256=sha_file(helper_source / '.github/scripts/hosted-frontend-receipt.py'),
                workflow_sha256=sha_file(helper_source / WORKFLOW_PATH))


def require_absent(inspect_result):
    """Only a registry not-found result permits publication; auth/network errors fail shut."""
    require(inspect_result.returncode != 0, 'exact SHA tag already exists; reconcile, never overwrite')
    error = inspect_result.stderr.lower()
    missing = ('manifest unknown' in error or TAG.lower() + ': not found' in error)
    unsafe = any(text in error for text in ('unauthorized', 'denied', 'forbidden', 'timeout',
                                           'connection', 'no such host', 'tls', '429', '500', '502', '503'))
    require(missing and not unsafe, 'tag absence is unproven; no publication allowed')


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
                    for layer in manifest['layers']:
                        descriptor_blob(layer)
                    candidates.append((descriptor, manifest))
        visit(index)
        require(len(candidates) == 1, 'expected exactly one genuine Linux/amd64 image')
        descriptor, manifest = candidates[0]
        return {'manifest_digest': descriptor['digest'], 'config_digest': manifest['config']['digest'],
                'platform': 'linux/amd64', 'layers': [dict(digest=x['digest'], bytes=x['size']) for x in manifest['layers']],
                'oci_archive_sha256': sha_file(archive), 'oci_archive_bytes': archive.stat().st_size,
                'revision_verified': True, 'all_referenced_blob_digests_verified': True}


def verify_registry(expected, push_metadata):
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
                  '--file', str(source / 'Dockerfile.frontend'), '--build-arg', 'BUILD_SHA=' + RELEASE,
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
        archive = out / 'frontend.oci.tar'
        export_cache = phase('oci', 'type=oci,oci-mediatypes=true,dest=' + str(archive), True)
        expected = oci_receipt(archive)
        write_new(out / 'oci-proof.json', dict(expected, cache=export_cache))
        # Source and final absence are checked immediately before the sole publication call.
        require(not command(['git', 'status', '--porcelain', '--untracked-files=all'], cwd=source), 'source changed during build')
        check_tag_absent()
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
                   'workflow': identity, 'expected_release': RELEASE,
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
        raise RuntimeError('one-shot frontend artifact failed; inspect compact failure and reconcile before any new attempt') from None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--execute', action='store_true')
    parser.add_argument('--source')
    parser.add_argument('--helper-source')
    parser.add_argument('--output')
    parser.add_argument('--builder')
    args = parser.parse_args()
    if not args.execute:
        print(json.dumps({'mode': 'plan_only', 'release': RELEASE, 'tag': TAG,
                          'branch': BRANCH, 'remote_actions': False}))
        return
    require(all((args.source, args.helper_source, args.output, args.builder)), 'explicit paths and owned builder required')
    execute(args)


if __name__ == '__main__':
    main()
