#!/usr/bin/env python3
"""Synthetic local verification only; never invokes Docker, GitHub, or a registry."""
import contextlib
import hashlib
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

SPEC = importlib.util.spec_from_file_location('receipt', Path(__file__).with_name('hosted-frontend-receipt.py'))
r = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(r)


def environment():
    commit = 'a' * 40
    return {'GITHUB_ACTIONS': 'true', 'GITHUB_REPOSITORY': 'assembledhq/143',
            'GITHUB_EVENT_NAME': 'push', 'GITHUB_REF': r.BRANCH, 'GITHUB_RUN_ATTEMPT': '1',
            'GITHUB_SHA': commit, 'GITHUB_WORKFLOW_SHA': commit,
            'GITHUB_WORKFLOW_REF': 'assembledhq/143/' + r.WORKFLOW_PATH + '@' + r.BRANCH,
            'GITHUB_RUN_ID': '123', 'GITHUB_JOB': 'frontend-artifact'}


def oci_fixture(path, *, revision=r.RELEASE, corrupt=False, duplicate=False, unsafe=False):
    blobs = {}
    def descriptor(data, media):
        digest = hashlib.sha256(data).hexdigest()
        blobs['blobs/sha256/' + digest] = data
        return {'digest': 'sha256:' + digest, 'size': len(data), 'mediaType': media}
    layer = descriptor(b'synthetic layer', 'application/vnd.oci.image.layer.v1.tar+gzip')
    config = descriptor(json.dumps({'os': 'linux', 'architecture': 'amd64',
                                   'config': {'Labels': {'org.opencontainers.image.revision': revision}}}).encode(),
                        'application/vnd.oci.image.config.v1+json')
    manifest = descriptor(json.dumps({'schemaVersion': 2, 'config': config, 'layers': [layer]}).encode(),
                          'application/vnd.oci.image.manifest.v1+json')
    blobs['index.json'] = json.dumps({'schemaVersion': 2, 'manifests': [manifest]}).encode()
    if corrupt:
        blobs['blobs/sha256/' + layer['digest'].split(':')[1]] = b'Synthetic layer'
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
            self.assertNotIn(str(temp), json.dumps(result))

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

    def test_public_workflow_has_no_runtime_or_credential_inputs(self):
        workflow = Path(__file__).parents[1] / 'workflows/hosted-frontend-build-once.yml'
        text = workflow.read_text()
        self.assertEqual(text.count('${{ secrets.'), 1)
        self.assertIn('${{ secrets.GITHUB_TOKEN }}', text)
        self.assertEqual(text.count('persist-credentials: false'), 2)
        self.assertIn('ref: ' + r.RELEASE, text)
        self.assertNotIn('workflow_dispatch:', text)
        self.assertNotIn('workflow_run:', text)
        self.assertNotIn('secrets: inherit', text)
        self.assertNotIn('latest', text.replace('ubuntu-latest', ''))
        self.assertNotIn('cache-to', text)
        self.assertIn('/public-receipt.json', text)


if __name__ == '__main__':
    unittest.main(verbosity=2)
