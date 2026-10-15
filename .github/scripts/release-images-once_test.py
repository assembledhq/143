#!/usr/bin/env python3
"""Synthetic local verification only; never invokes Docker, GitHub, or a registry."""
import contextlib
import hashlib
import gzip
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location('receipt', Path(__file__).with_name('release-images-once.py'))
r = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(r)


def bindings():
    return {'schema': 1, 'release': '1' * 40, 'tree': '2' * 40, 'archive_sha256': '3' * 64,
            'recipes': {name: '4' * 64 for name in ('Dockerfile', 'sandbox/Dockerfile', 'Dockerfile.frontend')}}


def environment():
    commit = 'a' * 40
    return {'GITHUB_ACTIONS': 'true', 'GITHUB_REPOSITORY': 'assembledhq/143',
            'GITHUB_EVENT_NAME': 'push', 'GITHUB_REF': r.BRANCH, 'GITHUB_RUN_ATTEMPT': '1',
            'GITHUB_SHA': commit, 'GITHUB_WORKFLOW_SHA': commit,
            'GITHUB_WORKFLOW_REF': 'assembledhq/143/' + r.WORKFLOW_PATH + '@' + r.BRANCH,
            'GITHUB_RUN_ID': '123', 'GITHUB_JOB': 'frontend-artifact'}


def layer_fixture(entries=None, *, raw=False):
    body = io.BytesIO()
    with tarfile.open(fileobj=body, mode='w', format=tarfile.PAX_FORMAT) as archive:
        for name, data, kind in entries or [('synthetic/file', b'synthetic layer', tarfile.REGTYPE)]:
            member = tarfile.TarInfo(name)
            member.type = kind
            member.size = len(data)
            archive.addfile(member, io.BytesIO(data) if kind == tarfile.REGTYPE else None)
    return body.getvalue() if raw else gzip.compress(body.getvalue())


def oci_fixture(path, *, revision=None, corrupt=False, duplicate=False, unsafe=False, layer_blob=None):
    revision = r.RELEASE if revision is None else revision
    blobs = {}
    def descriptor(data, media):
        digest = hashlib.sha256(data).hexdigest()
        blobs['blobs/sha256/' + digest] = data
        return {'digest': 'sha256:' + digest, 'size': len(data), 'mediaType': media}
    layer = descriptor(layer_fixture() if layer_blob is None else layer_blob, 'application/vnd.oci.image.layer.v1.tar+gzip')
    config = descriptor(json.dumps({'os': 'linux', 'architecture': 'amd64',
                                   'config': {'Labels': {'org.opencontainers.image.revision': revision}}}).encode(),
                        'application/vnd.oci.image.config.v1+json')
    manifest = descriptor(json.dumps({'schemaVersion': 2, 'config': config, 'layers': [layer]}).encode(),
                          'application/vnd.oci.image.manifest.v1+json')
    blobs['index.json'] = json.dumps({'schemaVersion': 2, 'manifests': [manifest]}).encode()
    if corrupt:
        key = 'blobs/sha256/' + layer['digest'].split(':')[1]
        blobs[key] = bytes([blobs[key][0] ^ 1]) + blobs[key][1:]
    if unsafe:
        blobs['../escape'] = b'x'
    with tarfile.open(path, 'w') as archive:
        for name, data in blobs.items():
            info = tarfile.TarInfo(name)
            info.size = len(data)
            archive.addfile(info, io.BytesIO(data))
        if duplicate:
            info = tarfile.TarInfo('index.json')
            info.size = len(blobs['index.json'])
            archive.addfile(info, io.BytesIO(blobs['index.json']))
    return manifest, config, layer


class ReceiptTests(unittest.TestCase):
    def setUp(self):
        r.configure(bindings(), 'frontend')

    def test_default_is_plan_only_without_subprocess(self):
        with mock.patch.object(sys, 'argv', ['receipt']), mock.patch.object(r.subprocess, 'run', side_effect=AssertionError('remote call')):
            with contextlib.redirect_stdout(io.StringIO()) as stream:
                r.main()
            self.assertEqual(json.loads(stream.getvalue())['remote_actions'], False)

    def test_context_binds_branch_workflow_and_first_attempt(self):
        self.assertEqual(r.context(environment())['workflow_commit'], 'a' * 40)
        for key, value in [('GITHUB_RUN_ATTEMPT', '2'), ('GITHUB_REF', 'refs/heads/main'),
                           ('GITHUB_WORKFLOW_SHA', 'b' * 40), ('GITHUB_EVENT_NAME', 'workflow_dispatch'),
                           ('GITHUB_REPOSITORY', 'synthetic/fork')]:
            with self.subTest(key=key):
                env = environment()
                env[key] = value
                with self.assertRaises(RuntimeError):
                    r.context(env)

    def test_absent_requires_specific_registry_not_found(self):
        for error in ['ERROR: ' + r.TAG + ': not found', 'manifest unknown']:
            with self.subTest(error=error):
                r.require_absent(subprocess.CompletedProcess([], 1, '', error))
        for code, error in [(0, ''), (1, 'builder not found'), (1, 'manifest unknown: unauthorized'),
                            (1, 'connection timeout'), (1, 'manifest unknown HTTP 503')]:
            with self.subTest(code=code, error=error):
                with self.assertRaises(RuntimeError):
                    r.require_absent(subprocess.CompletedProcess([], code, '', error))

    def test_missing_tag_digest_digits_are_not_http_errors(self):
        for digits in ('503', '429', '500', '502'):
            with self.subTest(digits=digits):
                pins = bindings()
                pins['release'] = 'a' * 17 + digits + 'b' * 20
                r.configure(pins, 'server')
                for error in ('ERROR: ' + r.TAG + ': not found',
                              'manifest unknown: release ' + r.RELEASE):
                    r.require_absent(subprocess.CompletedProcess([], 1, '', error))
                for problem in ('HTTP 503', 'HTTP503', 'HTTP/1.1 503', 'status code: 429',
                                'unauthorized', 'denied', 'forbidden', 'connection timeout',
                                'no such host', 'TLS failure'):
                    error = 'manifest unknown for ' + r.TAG + ': ' + problem
                    with self.assertRaises(RuntimeError):
                        r.require_absent(subprocess.CompletedProcess([], 1, '', error))

    def test_git_reads_disable_optional_locks_and_preserve_environment(self):
        with mock.patch.dict(r.os.environ, {'GIT_OPTIONAL_LOCKS': '1', 'SYNTHETIC_KEEP': 'yes'}, clear=True), \
                mock.patch.object(r.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, 'result\n', '')) as run:
            for argv in (['git', 'rev-parse', 'HEAD'], ['git', 'status', '--porcelain'],
                         ['git', 'ls-files', '--others'], ['git', 'archive', 'HEAD']):
                with self.subTest(argv=argv):
                    self.assertEqual(r.command(argv), 'result')
                    self.assertEqual(run.call_args.kwargs['env'], {'GIT_OPTIONAL_LOCKS': '0', 'SYNTHETIC_KEEP': 'yes'})
                    self.assertEqual(r.os.environ['GIT_OPTIONAL_LOCKS'], '1')

    def test_all_three_images_are_fixed_and_unbound_pins_refuse(self):
        for key, (image, recipe) in r.ALLOWLIST.items():
            with self.subTest(image=key):
                r.configure(bindings(), key)
                self.assertEqual(r.IMAGE, image)
                self.assertEqual(r.RECIPE_PATH, recipe)
                self.assertEqual(r.TAG, image + ':' + '1' * 40)
        for field in ('release', 'tree', 'archive_sha256'):
            with self.subTest(field=field):
                pins = bindings()
                pins[field] = 'UNBOUND'
                with self.assertRaises(RuntimeError):
                    r.configure(pins, 'server')
        for recipe in bindings()['recipes']:
            pins = bindings()
            pins['recipes'][recipe] = 'UNBOUND'
            with self.assertRaises(RuntimeError):
                r.configure(pins, 'server')
        with self.assertRaises(RuntimeError):
            r.configure(bindings(), 'other')

    def test_unbound_execute_fails_before_remote_or_builder(self):
        with tempfile.TemporaryDirectory() as temp:
            pins = bindings()
            pins['release'] = 'UNBOUND'
            path = Path(temp) / 'pins.json'
            path.write_text(json.dumps(pins))
            argv = ['receipt', '--execute', '--image', 'server', '--bindings', str(path)]
            with mock.patch.object(sys, 'argv', argv), \
                    mock.patch.object(r.subprocess, 'run', side_effect=AssertionError('remote call')):
                with self.assertRaises(RuntimeError):
                    r.main()

    def test_reconciliation_is_positive_read_only_and_never_claims_job_success(self):
        with tempfile.TemporaryDirectory() as temp:
            intent_path = Path(temp) / 'intent.json'
            output_path = Path(temp) / 'reconciliation.json'
            expected = {'platform': 'linux/amd64', 'revision_verified': True,
                        'all_referenced_blob_digests_verified': True}
            intent = {'schema': 1, 'result': 'publication_unresolved', 'image_key': r.IMAGE_KEY,
                      'tag': r.TAG, 'source': {'release': r.RELEASE, 'tree': r.TREE,
                      'source_archive_sha256': r.ARCHIVE, 'dockerfile_sha256': r.RECIPE}, 'oci': expected}
            intent_path.write_text(json.dumps(intent))
            args = type('Args', (), {'intent': str(intent_path), 'output': str(output_path)})()
            with mock.patch.object(r, 'verify_registry', return_value={'positive': True}) as registry, \
                    mock.patch.object(r, 'build', side_effect=AssertionError('build forbidden')), \
                    contextlib.redirect_stdout(io.StringIO()):
                r.reconcile(args)
            registry.assert_called_once_with(expected)
            receipt = json.loads(output_path.read_text())
            self.assertEqual(receipt['result'], 'positive_registry_reconciliation')
            self.assertFalse(receipt['publication_performed'])
            self.assertFalse(receipt['original_job_success'])
            self.assertNotIn(temp, json.dumps(receipt))
            intent['source']['release'] = 'b' * 40
            intent_path.write_text(json.dumps(intent))
            with mock.patch.object(r, 'verify_registry', side_effect=AssertionError('remote call')):
                with self.assertRaises(RuntimeError):
                    r.reconcile(args)

    def test_cache_proof_accepts_complete_cached_vertices(self):
        for field in ['id', 'digest']:
            with self.subTest(field=field):
                proof = r.CacheProof()
                proof.consume(json.dumps({field: 'vertex', 'name': '[builder 4/4] RUN npm run build'}))
                proof.consume(json.dumps({field: 'vertex', 'started': 'now', 'completed': 'now', 'cached': True}))
                proof.check_running()
                self.assertEqual(proof.finish(), {'source_run_vertices': 1, 'all_source_runs_cached': True})

    def test_uncached_or_missing_run_proof_refuses(self):
        for state in [{}, {'id': 'vertex', 'name': '[builder 4/4] RUN npm run build', 'started': 'now', 'completed': 'now'}]:
            with self.subTest(state=state):
                proof = r.CacheProof()
                proof.consume(json.dumps(state))
                with self.assertRaises(RuntimeError):
                    proof.finish()
                if state:
                    with self.assertRaises(RuntimeError):
                        proof.check_running()

    def test_fresh_oci_verifies_config_and_every_layer(self):
        with tempfile.TemporaryDirectory() as temp:
            archive = Path(temp) / 'synthetic.tar'
            manifest, config, layer = oci_fixture(archive)
            result = r.oci_receipt(archive)
            self.assertEqual(result['manifest_digest'], manifest['digest'])
            self.assertEqual(result['config_digest'], config['digest'])
            self.assertEqual(result['layers'], [{'digest': layer['digest'], 'bytes': layer['size']}])
            self.assertTrue(result['all_referenced_blob_digests_verified'])
            self.assertEqual(result['unpack_budget_by_layer'][0]['regular_file_uncompressed_bytes'], len(b'synthetic layer'))
            self.assertNotIn(str(temp), json.dumps(result))

    def test_streamed_layer_budget_is_explicit_conservative_and_contains_no_paths(self):
        blob = layer_fixture([('private/a/first', b'x', tarfile.REGTYPE),
                              ('private/b/second', b'y' * 4097, tarfile.REGTYPE)])
        actual = r.layer_unpack_budget(io.BytesIO(blob), 'application/vnd.oci.image.layer.v1.tar+gzip', len(blob))
        self.assertEqual(actual, {'compressed_blob_bytes': len(blob), 'regular_file_uncompressed_bytes': 4098,
            'regular_file_allocation_bytes_4096': 12288, 'tar_entry_count': 2,
            'implicit_parent_directory_inode_upper_bound': 4, 'inode_upper_bound': 6,
            'pax_metadata_allocation_bytes_4096': 0, 'metadata_allowance_bytes': 6 * 8192 + 1024**2,
            'estimated_download_and_unpacked_bytes': len(blob) + 12288 + 6 * 8192 + 1024**2,
            'metadata_bytes_per_inode': 8192, 'metadata_bytes_per_layer': 1024**2,
            'scope': 'local_overlay_estimate_not_physical_proof'})
        self.assertNotIn('private', json.dumps(actual))
        raw = layer_fixture(raw=True)
        self.assertGreater(r.layer_unpack_budget(io.BytesIO(raw), 'application/vnd.oci.image.layer.v1.tar', len(raw))['inode_upper_bound'], 0)

    def test_pax_metadata_payload_gets_a_separate_rounded_allowance(self):
        body = io.BytesIO()
        with tarfile.open(fileobj=body, mode='w', format=tarfile.PAX_FORMAT) as archive:
            member = tarfile.TarInfo('synthetic')
            member.pax_headers = {'SCHILY.xattr.synthetic': 'x' * 8193}
            archive.addfile(member)
        blob = gzip.compress(body.getvalue())
        result = r.layer_unpack_budget(io.BytesIO(blob), 'application/vnd.oci.image.layer.v1.tar+gzip', len(blob))
        self.assertEqual(result['pax_metadata_allocation_bytes_4096'], 12288)
        self.assertEqual(result['tar_entry_count'], 2)
        self.assertEqual(result['metadata_allowance_bytes'], 2 * 8192 + 1024**2 + 12288)
        self.assertNotIn('SCHILY', json.dumps(result))

    def test_unknown_sparse_pax_version_is_refused(self):
        body = io.BytesIO()
        with tarfile.open(fileobj=body, mode='w', format=tarfile.PAX_FORMAT) as archive:
            member = tarfile.TarInfo('synthetic')
            member.pax_headers = {'GNU.sparse.major': '2', 'GNU.sparse.minor': '0'}
            archive.addfile(member)
        blob = gzip.compress(body.getvalue())
        with self.assertRaises(RuntimeError):
            r.layer_unpack_budget(io.BytesIO(blob), 'application/vnd.oci.image.layer.v1.tar+gzip', len(blob))

    def test_layer_budget_refuses_invalid_paths_formats_limits_and_corrupt_gzip(self):
        media = 'application/vnd.oci.image.layer.v1.tar+gzip'
        for path in ('/escape', '../escape', 'a/../escape', 'a' * 4097):
            with self.subTest(path_length=len(path)), self.assertRaises(RuntimeError):
                blob = layer_fixture([(path, b'x', tarfile.REGTYPE)])
                r.layer_unpack_budget(io.BytesIO(blob), media, len(blob))
        for kind in (tarfile.GNUTYPE_SPARSE, b'?'):
            with self.subTest(kind=kind), self.assertRaises(RuntimeError):
                blob = layer_fixture([('synthetic', b'', kind)])
                r.layer_unpack_budget(io.BytesIO(blob), media, len(blob))
        blob = layer_fixture()
        with self.assertRaises(RuntimeError):
            r.layer_unpack_budget(io.BytesIO(blob), 'application/vnd.oci.image.layer.v1.tar+zstd', len(blob))
        for key, limit in (('MAX_TAR_ENTRIES', 0), ('MAX_LAYER_UNCOMPRESSED_BYTES', 512),
                           ('MAX_BUDGET_BYTES', 1)):
            with self.subTest(limit=key), mock.patch.object(r, key, limit), self.assertRaises(RuntimeError):
                r.layer_unpack_budget(io.BytesIO(blob), media, len(blob))
        corrupt = blob[:-4] + bytes([blob[-4] ^ 1]) + blob[-3:]
        with self.assertRaises((OSError, EOFError, tarfile.TarError, RuntimeError)):
            r.layer_unpack_budget(io.BytesIO(corrupt), media, len(corrupt))
        raw = layer_fixture(raw=True)
        invalid = raw[:1024] + b'not a tar header' + b'\x00' * (len(raw) - 1040)
        with self.assertRaises(RuntimeError):
            r.layer_unpack_budget(io.BytesIO(invalid), 'application/vnd.oci.image.layer.v1.tar', len(invalid))

    def test_bad_oci_is_refused_without_extraction(self):
        cases = [('corrupt', {'corrupt': True}), ('duplicate', {'duplicate': True}),
                 ('unsafe', {'unsafe': True}), ('wrong revision', {'revision': 'b' * 40})]
        for name, options in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                archive = Path(temp) / 'synthetic.tar'
                oci_fixture(archive, **options)
                with self.assertRaises(RuntimeError):
                    r.oci_receipt(archive)
                self.assertEqual([p.name for p in Path(temp).iterdir()], ['synthetic.tar'])

    def test_registry_receipt_requires_matching_actual_config(self):
        expected = {'manifest_digest': 'sha256:' + 'a' * 64, 'config_digest': 'sha256:' + 'b' * 64,
                    'layers': [{'digest': 'sha256:' + 'c' * 64, 'bytes': 7}]}
        metadata = {'containerimage.digest': expected['manifest_digest'],
                    'containerimage.config.digest': expected['config_digest']}
        raw = {'config': {'digest': expected['config_digest']},
               'layers': [{'digest': 'sha256:' + 'c' * 64, 'size': 7}]}
        config = {'os': 'linux', 'architecture': 'amd64',
                  'config': {'Labels': {'org.opencontainers.image.revision': r.RELEASE}}}
        for config_digest in [expected['config_digest'], 'sha256:' + 'd' * 64]:
            with self.subTest(config_digest=config_digest):
                raw['config']['digest'] = config_digest
                outputs = [json.dumps({'digest': expected['manifest_digest']}), json.dumps(raw), json.dumps(config)]
                with mock.patch.object(r, 'command', side_effect=outputs):
                    if config_digest == expected['config_digest']:
                        self.assertTrue(r.verify_registry(expected, metadata)['fresh_oci_and_registry_match'])
                    else:
                        with self.assertRaises(RuntimeError):
                            r.verify_registry(expected, metadata)

    def test_failure_receipt_omits_exception_details_and_private_paths(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / 'attempt'
            args = type('Args', (), {'source': temp, 'helper_source': temp,
                                    'output': str(output), 'builder': 'owned-test-builder'})()
            with mock.patch.dict(r.os.environ, environment(), clear=True), \
                    mock.patch.object(r, 'verify_source', side_effect=RuntimeError('synthetic-private-detail')), \
                    mock.patch.object(r.subprocess, 'run', side_effect=AssertionError('remote call')):
                with contextlib.redirect_stdout(io.StringIO()), self.assertRaises(RuntimeError):
                    r.execute(args)
            failure = json.loads((output / 'public-failure.json').read_text())
            self.assertEqual(failure['phase'], 'source_or_admission')
            self.assertFalse(failure['source_proof_written'])
            self.assertFalse(failure['publication_may_have_started'])
            self.assertNotIn('synthetic-private-detail', json.dumps(failure))
            self.assertNotIn(temp, json.dumps(failure))

    def test_oversized_layer_member_refuses_before_any_publication_intent(self):
        member = tarfile.TarInfo('synthetic')
        member.size = r.MAX_LAYER_UNCOMPRESSED_BYTES + 1
        invalid_layer = gzip.compress(member.tobuf(format=tarfile.GNU_FORMAT) + b'\x00' * 1024)
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / 'attempt'
            args = type('Args', (), {'source': temp, 'helper_source': temp,
                                    'output': str(output), 'builder': 'owned-test-builder'})()
            calls = []
            def synthetic_build(argv, log, *, cached=False):
                destination = argv[argv.index('--output') + 1]
                calls.append(destination)
                Path(argv[argv.index('--metadata-file') + 1]).write_text(json.dumps({'buildx.build.ref': 'fixture'}))
                if destination.startswith('type=oci,'):
                    oci_fixture(Path(destination.split('dest=', 1)[1]), layer_blob=invalid_layer)
                return {'all_source_runs_cached': True} if cached else {'cache_only_completed': True}
            with mock.patch.dict(r.os.environ, environment(), clear=True), \
                    mock.patch.object(r, 'verify_source', return_value={'synthetic': True}), \
                    mock.patch.object(r, 'check_tag_absent'), \
                    mock.patch.object(r, 'build', side_effect=synthetic_build), \
                    mock.patch.object(r.subprocess, 'run', side_effect=AssertionError('remote call')), \
                    contextlib.redirect_stdout(io.StringIO()), self.assertRaises(RuntimeError):
                r.execute(args)
            self.assertEqual(len(calls), 2)
            self.assertFalse((output / 'push-intent.json').exists())
            self.assertFalse((output / 'public-intent.json').exists())
            self.assertFalse((output / 'public-receipt.json').exists())
            failure = json.loads((output / 'public-failure.json').read_text())
            self.assertEqual(failure['phase'], 'oci')
            self.assertFalse(failure['publication_may_have_started'])

    def test_unknown_push_keeps_genuine_intent_and_never_claims_success(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / 'attempt'
            args = type('Args', (), {'source': temp, 'helper_source': temp,
                                    'output': str(output), 'builder': 'owned-test-builder'})()
            source = {'release': r.RELEASE, 'tree': r.TREE, 'source_archive_sha256': r.ARCHIVE,
                      'dockerfile_sha256': r.RECIPE}
            def synthetic_build(argv, log, *, cached=False):
                metadata = Path(argv[argv.index('--metadata-file') + 1])
                metadata.write_text(json.dumps({'buildx.build.ref': 'synthetic-fixture-build'}))
                destination = argv[argv.index('--output') + 1]
                if destination.startswith('type=oci,'):
                    oci_fixture(Path(destination.split('dest=', 1)[1]))
                return {'all_source_runs_cached': True} if cached else {'cache_only_completed': True}
            with mock.patch.dict(r.os.environ, environment(), clear=True), \
                    mock.patch.object(r, 'verify_source', return_value=source), \
                    mock.patch.object(r, 'command', return_value=''), \
                    mock.patch.object(r, 'check_tag_absent') as absent, \
                    mock.patch.object(r, 'build', side_effect=synthetic_build), \
                    mock.patch.object(r, 'verify_registry', side_effect=RuntimeError('synthetic-unknown-publication')), \
                    mock.patch.object(r.subprocess, 'run', side_effect=AssertionError('remote call')), \
                    contextlib.redirect_stdout(io.StringIO()), self.assertRaises(RuntimeError):
                r.execute(args)
            self.assertEqual(absent.call_count, 2)
            intent = json.loads((output / 'public-intent.json').read_text())
            self.assertEqual(intent['result'], 'publication_unresolved')
            self.assertTrue(intent['oci']['all_referenced_blob_digests_verified'])
            self.assertEqual(intent['oci']['manifest_digest'], r.oci_receipt(output / 'frontend.oci.tar')['manifest_digest'])
            self.assertFalse((output / 'public-receipt.json').exists())
            failure = json.loads((output / 'public-failure.json').read_text())
            self.assertTrue(failure['publication_may_have_started'])
            self.assertNotIn('synthetic-unknown-publication', json.dumps(failure))

    def test_public_workflow_has_no_runtime_or_credential_inputs(self):
        workflow = Path(__file__).parents[1] / 'workflows/release-images-once.yml'
        if not workflow.exists():
            workflow = Path(str(workflow) + '.in')
        text = workflow.read_text()
        self.assertEqual(text.count('${{ secrets.'), 1)
        self.assertIn('${{ secrets.GITHUB_TOKEN }}', text)
        self.assertEqual(text.count('persist-credentials: false'), 2)
        pins = json.loads(Path(__file__).with_name('release-image-bindings.json').read_text())
        release_ref = '@@RELEASE_SHA@@' if pins['release'] == 'UNBOUND' else pins['release']
        self.assertIn('ref: ' + release_ref, text)
        self.assertNotIn('workflow_dispatch:', text)
        self.assertNotIn('workflow_run:', text)
        self.assertNotIn('secrets: inherit', text)
        self.assertNotIn('latest', text.replace('ubuntu-latest', ''))
        self.assertNotIn('cache-to', text)
        self.assertIn('/public-receipt.json', text)


if __name__ == '__main__':
    unittest.main(verbosity=2)
